package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GlobalCleanOpts holds the options for CmdGlobalClean. DryRun is
// read from App.DryRun (consistent with sync/init); options here
// are only for filtering which tools to clean.
type GlobalCleanOpts struct {
	// Targets, when non-empty, filters which tools' global dirs to
	// clean. Aliases honored via ResolveTool.
	Targets []string
}

// CmdGlobalClean removes the per-tool global filesystem artifacts
// that `global sync` would create — symlinks into ~/.agents/ and
// concat files carrying the sync-agents banner — and prunes empty
// parent directories.
//
// Safety contract (SPEC-002 §Requirement: Global clean):
//
//   - Symlinks are removed ONLY if their target resolves inside the
//     global root. A user-curated symlink to elsewhere stays put.
//   - Regular files are removed ONLY if their head bytes contain
//     the sync-agents banner. A user-written file at the same path
//     (someone manually editing instructions.md, say) is left alone
//     with a warning.
//   - The canonical ~/.agents/ tree is never touched.
//   - Directories created by sync-agents that become empty after
//     symlink/concat removal are rmdir'd, walking upward until a
//     non-empty parent is reached or the per-tool root itself.
//
// App.DryRun causes the planned operations to print without any
// filesystem writes.
//
// Returns the first error encountered, after attempting to clean
// every tool. Per-tool errors are logged but do not abort the loop
// — partial progress is better than all-or-nothing for cleanup.
//
// See docs/commands/global-clean.md for user-facing documentation.
func (a *App) CmdGlobalClean(opts GlobalCleanOpts) error {
	root := a.ResolveGlobalRoot()
	parent := a.ResolveGlobalRootParent()

	if _, err := os.Stat(root); err != nil {
		a.Error(fmt.Sprintf("global root %s does not exist; nothing to clean", root))
		return err
	}

	tools, err := a.resolveSyncTools(opts.Targets)
	if err != nil {
		return err
	}

	totalRemoved := 0
	for _, tool := range tools {
		dir := tool.DirForScope(ScopeGlobal, parent)
		if dir == "" {
			continue
		}
		removed, err := a.cleanToolDir(tool.ID, dir, root)
		if err != nil {
			a.Warn(fmt.Sprintf("[%s] %v", tool.ID, err))
		}
		totalRemoved += removed
	}

	// Hooks clean — removes sync-agents-owned entries from
	// .claude/settings.json (SPEC-004 Part C). This runs after the
	// per-tool directory walk because hooks don't live in a
	// per-tool dir — they're merged into settings.json.
	if a.isToolActive("claude", tools) {
		settingsPath := filepath.Join(parent, ".claude", "settings.json")
		statePath := filepath.Join(root, ".sync", "claude-hooks-state.json")
		if a.DryRun {
			if fileExists(statePath) {
				a.Info(fmt.Sprintf("[dry-run] would clean hook entries from %s", settingsPath))
			}
		} else {
			n, err := a.CleanHooks(settingsPath, statePath)
			if err != nil {
				a.Warn(fmt.Sprintf("hooks clean: %v", err))
			} else if n > 0 {
				totalRemoved += n
				a.Info(fmt.Sprintf("hooks: removed %d entry/ies from %s", n, settingsPath))
			}
		}
	}

	if a.DryRun {
		a.Info(fmt.Sprintf("[dry-run] would remove %d item(s) across %d tool(s)", totalRemoved, len(tools)))
	} else {
		a.Info(fmt.Sprintf("removed %d item(s) across %d tool(s)", totalRemoved, len(tools)))
	}
	return nil
}

// cleanToolDir walks one tool's per-scope directory and removes
// sync-agents-owned artifacts. Returns the count of items removed
// (or that would be removed in dry-run mode).
//
// Walk order is depth-first so file removal happens before directory
// removal — that way the empty-parent pruning at the end sees the
// actual final state.
func (a *App) cleanToolDir(toolID, dir, agentsRoot string) (int, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("not a directory: %s", dir)
	}

	removed := 0
	// Track candidate parents to prune at the end. Sorted by depth
	// (longest path first) so we prune leaves before their parents.
	var pruneCandidates []string

	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == dir {
			return nil
		}

		info, err := os.Lstat(path)
		if err != nil {
			return nil // skip unreadable entries; not fatal
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if !symlinkPointsInto(path, agentsRoot) {
				return nil // user-curated symlink — leave alone
			}
			if a.DryRun {
				a.Info(fmt.Sprintf("[dry-run] [%s] would remove symlink %s", toolID, path))
				removed++
				pruneCandidates = append(pruneCandidates, filepath.Dir(path))
				return nil
			}
			if err := os.Remove(path); err != nil {
				a.Warn(fmt.Sprintf("[%s] failed to remove %s: %v", toolID, path, err))
				return nil
			}
			removed++
			pruneCandidates = append(pruneCandidates, filepath.Dir(path))
			return nil
		}

		if info.IsDir() {
			return nil
		}

		// Regular file: check for the sync-agents banner. Files
		// without it are user-owned.
		if !fileCarriesBanner(path) {
			a.Warn(fmt.Sprintf("[%s] skip non-sync-agents file %s", toolID, path))
			return nil
		}
		if a.DryRun {
			a.Info(fmt.Sprintf("[dry-run] [%s] would remove concat %s", toolID, path))
			removed++
			pruneCandidates = append(pruneCandidates, filepath.Dir(path))
			return nil
		}
		if err := os.Remove(path); err != nil {
			a.Warn(fmt.Sprintf("[%s] failed to remove %s: %v", toolID, path, err))
			return nil
		}
		removed++
		pruneCandidates = append(pruneCandidates, filepath.Dir(path))
		return nil
	})
	if err != nil {
		return removed, err
	}

	// Prune now-empty directories. Sort by descending depth so we
	// rmdir leaves before their parents. Stop at dir itself (the
	// tool's per-scope root); leave that alone unless it's a
	// generated subdir like global_workflows/ that's now empty.
	a.pruneEmptyDirs(toolID, pruneCandidates, dir)

	// For Claude specifically, the managed @-import block lives in
	// <dir>/CLAUDE.md. This file is user-editable, so we don't
	// remove it blindly during the walk (fileCarriesBanner would
	// reject it because its first line is an HTML comment marker,
	// not the concat banner). After the walk has pruned every
	// other sync-agents artifact, give the CLAUDE.md one more pass
	// — if it carries a managed block, strip the block. If the
	// file is empty afterwards, remove it; that may re-open the
	// per-tool root for pruning below.
	if toolID == "claude" {
		claudeMD := filepath.Join(dir, "CLAUDE.md")
		if _, _, err := a.scrubClaudeManagedBlock(claudeMD, a.DryRun); err != nil {
			a.Warn(fmt.Sprintf("[%s] failed to scrub CLAUDE.md: %v", toolID, err))
		}
	}

	// Also pluck the per-tool root itself if it's now empty.
	// The Claude scrub above may have removed CLAUDE.md, making
	// the dir empty at this point — so this check runs last.
	if a.dirIsEmpty(dir) {
		if a.DryRun {
			a.Info(fmt.Sprintf("[dry-run] [%s] would rmdir empty %s", toolID, dir))
		} else {
			if err := os.Remove(dir); err != nil {
				// Non-fatal — leave the empty dir alone.
				a.Warn(fmt.Sprintf("[%s] could not rmdir empty %s: %v", toolID, dir, err))
			}
		}
	}

	return removed, nil
}

// claudeManagedRegions are the marker pairs global sync writes into a
// global CLAUDE.md: the @-import block and the AGENTS.md mirror.
var claudeManagedRegions = [][2]string{
	{ManagedImportBlockStart, ManagedImportBlockEnd},
	{AgentsMDBlockStart, AgentsMDBlockEnd},
}

// removeRegion cuts the first startMarker…endMarker region (and the
// newline after it) out of content, reporting whether one was found.
func removeRegion(content, startMarker, endMarker string) (string, bool) {
	startIdx := strings.Index(content, startMarker)
	if startIdx < 0 {
		return content, false
	}
	endIdx := strings.Index(content[startIdx:], endMarker)
	if endIdx < 0 {
		return content, false
	}
	endFull := startIdx + endIdx + len(endMarker)
	if endFull < len(content) && content[endFull] == '\n' {
		endFull++
	}
	return content[:startIdx] + content[endFull:], true
}

// scrubClaudeManagedBlock reads the file at claudeMDPath, strips every
// region global sync manages there (see claudeManagedRegions), and
// either rewrites the file without them or removes the file outright
// if stripping would leave it empty (whitespace-only). Returns
// (removed, dirPruned, error):
//
//   - removed: 1 if the scrub wrote or removed something, 0 if the
//     file had no managed region. Used by clean's running total.
//   - dirPruned: true when the file was removed and its parent dir
//     may now have become empty. Useful for dry-run reporting.
//
// When the file has content outside the markers, only the regions are
// stripped; the rest is preserved. This protects user-authored
// frontmatter or prose that may co-exist with the managed regions.
func (a *App) scrubClaudeManagedBlock(claudeMDPath string, dryRun bool) (int, bool, error) {
	data, err := os.ReadFile(claudeMDPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}

	remaining := string(data)
	found := false
	// Repeat until no region is left: files written by older versions
	// can hold a region more than once, including one nested inside the
	// other.
	for cut := true; cut; {
		cut = false
		for _, region := range claudeManagedRegions {
			var removed bool
			remaining, removed = removeRegion(remaining, region[0], region[1])
			cut = cut || removed
		}
		found = found || cut
	}
	if !found {
		return 0, false, nil
	}

	if dryRun {
		a.Info(fmt.Sprintf("[dry-run] would scrub managed blocks from %s", claudeMDPath))
		return 1, false, nil
	}

	// Collapse any resulting double-blank-lines into a single one so
	// the preserved regions don't carry awkward whitespace after scrub.
	for strings.Contains(remaining, "\n\n\n") {
		remaining = strings.ReplaceAll(remaining, "\n\n\n", "\n\n")
	}
	remaining = strings.TrimSpace(remaining)

	if remaining == "" {
		// Whole file was managed regions — drop the file so the
		// per-tool root's empty-dir check can prune .claude/.
		if err := os.Remove(claudeMDPath); err != nil {
			return 0, false, err
		}
		return 1, true, nil
	}

	// Had user content outside the regions — rewrite without them.
	if !strings.HasSuffix(remaining, "\n") {
		remaining += "\n"
	}
	if err := os.WriteFile(claudeMDPath, []byte(remaining), 0o644); err != nil {
		return 0, false, err
	}
	return 1, false, nil
}

// pruneEmptyDirs walks the candidate parent paths from longest to
// shortest and rmdir's any that are empty. Stops at the per-tool
// root (passed as `stop`) which is handled by the caller.
func (a *App) pruneEmptyDirs(toolID string, candidates []string, stop string) {
	// Deduplicate.
	seen := map[string]bool{}
	var uniq []string
	for _, p := range candidates {
		if seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}
	// Sort longest path first.
	sort.Slice(uniq, func(i, j int) bool { return len(uniq[i]) > len(uniq[j]) })

	for _, p := range uniq {
		if p == stop || !strings.HasPrefix(p, stop+string(filepath.Separator)) {
			continue
		}
		if !a.dirIsEmpty(p) {
			continue
		}
		if a.DryRun {
			a.Info(fmt.Sprintf("[dry-run] [%s] would rmdir empty %s", toolID, p))
			continue
		}
		if err := os.Remove(p); err != nil {
			// Non-fatal: a concurrent process or a permissions
			// quirk could prevent removal. Log and continue.
			a.Warn(fmt.Sprintf("[%s] could not rmdir %s: %v", toolID, p, err))
		}
		// After removing this dir, its parent might also be empty.
		// We don't recurse here; the next sync-agents run will
		// clean it up. Keeping the loop simple beats chasing rare
		// nesting cases.
	}
}

// dirIsEmpty reports whether the directory at path has no entries.
// Returns false on stat errors so we don't accidentally try to
// remove a directory we can't read.
func (a *App) dirIsEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) == 0
}

// symlinkPointsInto reports whether the symlink at linkPath
// resolves to a path inside agentsRoot. Used to gate symlink
// removal: only links into the canonical tree are sync-agents'
// responsibility.
//
// The check is on the lexical target, not the resolved target —
// EvalSymlinks would follow chains and could yield surprising
// results on broken links. Lexical comparison is safer and matches
// how `global sync` writes the links (absolute paths from the
// resolver).
func symlinkPointsInto(linkPath, agentsRoot string) bool {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	// Match either exact equality with agentsRoot or a path prefix
	// of agentsRoot + separator. Exact equality would only happen
	// if someone linked the entire ~/.agents/ tree, which is
	// pathological but harmless to handle.
	if abs == agentsRoot {
		return true
	}
	return strings.HasPrefix(abs, agentsRoot+string(filepath.Separator))
}

// fileCarriesBanner returns true if the first few bytes of the file
// match the sync-agents banner. Used to gate concat file removal.
//
// We read up to 256 bytes — enough to cover the banner plus some
// slack. A file with the banner anywhere except the very top is
// considered user-owned (we wrote the banner first; deviation means
// the user touched it).
func fileCarriesBanner(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 256)
	n, _ := f.Read(head)
	return bytes.HasPrefix(head[:n], []byte("<!--\nGenerated by sync-agents"))
}

// isToolActive reports whether the tool with the given ID is in the
// resolved tools slice. Used to gate per-tool post-processing
// (e.g. hooks clean) that only runs when the target tool was
// included in the sync.
func (a *App) isToolActive(toolID string, tools []Tool) bool {
	for _, t := range tools {
		if t.ID == toolID {
			return true
		}
	}
	return false
}
