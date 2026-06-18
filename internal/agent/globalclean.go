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
		// Tool-specific pre-walk cleanup. For Claude, strip the
		// managed @-imports block (and delete CLAUDE.md if no user
		// content remains) BEFORE cleanToolDir runs — that way its
		// empty-parent prune picks up a now-empty .claude/.
		if tool.ID == "claude" {
			n, err := a.cleanClaudeImportsBlock(parent)
			if err != nil {
				a.Warn(fmt.Sprintf("[%s] Claude @-imports cleanup: %v", tool.ID, err))
			}
			totalRemoved += n
		}

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

	if a.DryRun {
		a.Info(fmt.Sprintf("[dry-run] would remove %d item(s) across %d tool(s)", totalRemoved, len(tools)))
	} else {
		a.Info(fmt.Sprintf("removed %d item(s) across %d tool(s)", totalRemoved, len(tools)))
	}
	return nil
}

// cleanClaudeImportsBlock removes the managed `@`-imports block
// (see claude_imports.go) from ~/.claude/CLAUDE.md, preserving any
// content the user wrote outside the markers. If stripping the block
// leaves the file with no remaining user content (whitespace only),
// the file itself is deleted so the parent .claude/ becomes eligible
// for the standard empty-parent prune that cleanToolDir performs at
// its tail.
//
// Returns 1 if an action was taken (or would be in dry-run), 0
// otherwise. Skips silently when CLAUDE.md does not exist, or when
// it exists but has no managed block (i.e. nothing for us to clean).
func (a *App) cleanClaudeImportsBlock(parent string) (int, error) {
	claudeMdPath := filepath.Join(parent, ".claude", "CLAUDE.md")
	data, err := os.ReadFile(claudeMdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	content := string(data)
	if !strings.Contains(content, ClaudeImportsBlockStart) {
		return 0, nil // not ours to clean
	}
	stripped := RemoveClaudeImportBlock(content)

	if a.DryRun {
		if strings.TrimSpace(stripped) == "" {
			a.Info(fmt.Sprintf("[dry-run] would remove %s (no remaining user content)", claudeMdPath))
		} else {
			a.Info(fmt.Sprintf("[dry-run] would strip Claude @-imports block from %s", claudeMdPath))
		}
		return 1, nil
	}

	if strings.TrimSpace(stripped) == "" {
		if err := os.Remove(claudeMdPath); err != nil {
			return 0, err
		}
		a.Info(fmt.Sprintf("removed %s (no remaining user content)", claudeMdPath))
		return 1, nil
	}
	if err := os.WriteFile(claudeMdPath, []byte(stripped), 0o644); err != nil {
		return 0, err
	}
	a.Info(fmt.Sprintf("stripped Claude @-imports block from %s", claudeMdPath))
	return 1, nil
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

	// Also pluck the per-tool root itself if it's now empty.
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
