package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GlobalSyncOpts holds the per-call options for CmdGlobalSync. The
// zero value is "sync all registered tools, no force, no dry-run".
//
// Targets, when non-empty, filters the set of tools to operate on.
// Names are matched against each Tool's ID and Aliases (so
// {"windsurf"} resolves to the codeium Tool). Unknown names are
// flagged as errors but do not abort the whole sync.
type GlobalSyncOpts struct {
	Targets []string
}

// CmdGlobalSync fans the user's ~/.agents/ tree out into per-tool
// global directories per SPEC-002's semantic-aware routing.
//
// At a high level, for each artifact under ~/.agents/{rules,skills,
// workflows}/ and each tool in scope, the sync:
//
//  1. Resolves the artifact's Semantic via frontmatter + bucket
//     default (see semantic.go).
//  2. Resolves the per-tool Destination via TargetDestination
//     (see destination.go).
//  3. For StrategySymlink destinations, creates or repairs the
//     symlink. Existing non-symlink files at the destination are
//     skipped unless --force is set on the App.
//  4. For StrategyConcat destinations, accumulates the artifact into
//     a per-concat-Path batch.
//  5. For StrategySkip destinations, prints a warning and moves on.
//
// After the per-artifact pass, RegenerateConcat is called once per
// concat target with its accumulated entries, producing one atomic
// write per target. Concat files preserve mtime when content is
// unchanged (idempotency).
//
// The function honors App.DryRun: in dry-run mode no filesystem
// writes occur and a "[dry-run]" prefix is printed for each planned
// op.
//
// The function honors App.Force: an existing non-symlink at a
// symlink destination is replaced (after a backup-rename); without
// Force such conflicts are skipped with a warning.
//
// See docs/commands/global-sync.md for user-facing documentation
// and SPEC-002 §Requirement: Global sync + §Requirement: Semantic-
// aware routing for the requirements.
func (a *App) CmdGlobalSync(opts GlobalSyncOpts) error {
	root := a.ResolveGlobalRoot()
	parent := a.ResolveGlobalRootParent()

	if _, err := os.Stat(root); err != nil {
		a.Error(fmt.Sprintf("global root %s does not exist; run `sync-agents global init` first", root))
		return err
	}

	tools, err := a.resolveSyncTools(opts.Targets)
	if err != nil {
		return err
	}

	// Discover every artifact under ~/.agents/. We do this once and
	// reuse the slice across tools.
	artifacts, err := DiscoverArtifacts(root)
	if err != nil {
		return err
	}
	// Determine hasClaudeTarget before the early-return check so
	// we know whether to bail when there are only hooks.
	hasClaudeTarget := false
	for _, t := range tools {
		if t.ID == "claude" {
			hasClaudeTarget = true
			break
		}
	}

	if len(artifacts) == 0 {
		// Only bail when there are truly no artifacts AND no hooks.
		// Hooks aren't discovered as Artifacts (they batch-merge
		// into settings.json), so an empty artifact slice doesn't
		// mean nothing to do when .agents/hooks/ has content.
		if !hasClaudeTarget || !dirExists(filepath.Join(root, "hooks")) {
			a.Info("no artifacts under " + root + "; nothing to sync")
			return nil
		}
	}

	// concatBatches accumulates entries per concat-Path so we can
	// regenerate each target once after the artifact pass.
	concatBatches := map[string][]ConcatEntry{}

	// claudeRouted collects every (name, sem, typ) that routed to
	// Claude during this sync. The passive-rule subset drives the
	// managed @-import block we write to ~/.claude/CLAUDE.md after
	// the artifact pass — Claude doesn't auto-load rules/*.md, so
	// the block is how passive rules actually reach Claude's
	// context. See SPEC-002 §Claude rule loading (issue #46).
	var claudeRouted []ClaudeRoutedArtifact

	a.Info(fmt.Sprintf("syncing %d artifact(s) to %d tool(s)", len(artifacts), len(tools)))

	for _, art := range artifacts {
		// Resolve Semantic once per artifact, not per tool —
		// semantic is a property of the artifact, not the
		// destination.
		sem, err := ResolveSemantic(art.SourcePath, art.Type)
		if err != nil {
			a.Warn(fmt.Sprintf("skipping %s/%s: %v", art.Type, art.Name, err))
			continue
		}

		for _, tool := range tools {
			dest := TargetDestination(tool, art.Type, art.Name, sem, art.SourcePath, parent)
			switch dest.Strategy {
			case StrategySkip:
				a.Warn(fmt.Sprintf("[%s] skip %s %q: %s", tool.ID, art.Type, art.Name, dest.SkipReason))

			case StrategySymlink:
				if tool.ID == "claude" {
					claudeRouted = append(claudeRouted, ClaudeRoutedArtifact{
						Type:     art.Type,
						Name:     art.Name,
						Semantic: sem,
					})
				}
				if err := a.applySymlinkDestination(tool.ID, art, dest); err != nil {
					a.Warn(fmt.Sprintf("[%s] %s %q: %v", tool.ID, art.Type, art.Name, err))
				}

			case StrategyConcat:
				concatBatches[dest.Path] = append(concatBatches[dest.Path], ConcatEntry{
					Name:       art.Name,
					SourcePath: concatSourcePath(art),
				})
			}
		}
	}

	// After the per-artifact pass — and after concat regen below —
	// write the managed @-import block for Claude. The block is the
	// bridge between "sync placed the rule at ~/.claude/rules/X.md"
	// and "Claude actually loads rule X into context." Without it,
	// passive rules reach the filesystem but not the model.
	if hasClaudeTarget && !a.DryRun {
		claudeMDPath := filepath.Join(parent, ".claude", "CLAUDE.md")

		// Mirror the global AGENTS.md — which already carries the
		// injected AGENTS.preamble.md — into CLAUDE.md as its own
		// managed block. Without this, preamble edits reach AGENTS.md
		// but never the global CLAUDE.md, so every session outside the
		// agents repo keeps loading stale principles.
		agentsMDPath := ResolveGlobalAgentsMDPath(a.ResolveGlobalRoot())
		if agentsMDPath != "" {
			changed, err := RegenerateAgentsMDBlock(claudeMDPath, agentsMDPath)
			if err != nil {
				a.Warn(fmt.Sprintf("AGENTS.md mirror failed for %s: %v", claudeMDPath, err))
			} else if changed {
				a.Info(fmt.Sprintf("mirrored %s into %s", agentsMDPath, claudeMDPath))
			} else {
				a.Info(fmt.Sprintf("%s AGENTS.md mirror already current", claudeMDPath))
			}
		}

		importPaths := CollectClaudeRuleImportPaths(parent, claudeRouted)
		if len(importPaths) > 0 {
			changed, err := RegenerateClaudeImports(claudeMDPath, importPaths)
			if err != nil {
				a.Warn(fmt.Sprintf("claude imports regen failed for %s: %v", claudeMDPath, err))
			} else if changed {
				a.Info(fmt.Sprintf("regenerated %s (%d @-imports)", claudeMDPath, len(importPaths)))
			} else {
				a.Info(fmt.Sprintf("%s already current (%d @-imports)", claudeMDPath, len(importPaths)))
			}
		}
	} else if hasClaudeTarget && a.DryRun {
		claudeMDPath := filepath.Join(parent, ".claude", "CLAUDE.md")
		if agentsMDPath := ResolveGlobalAgentsMDPath(a.ResolveGlobalRoot()); agentsMDPath != "" {
			a.Info(fmt.Sprintf("[dry-run] would mirror %s into %s", agentsMDPath, claudeMDPath))
		}
		importPaths := CollectClaudeRuleImportPaths(parent, claudeRouted)
		if len(importPaths) > 0 {
			a.Info(fmt.Sprintf("[dry-run] would regenerate %s with %d @-imports", claudeMDPath, len(importPaths)))
		}
	}

	// Regenerate each concat target. Sort by path for deterministic
	// log output across runs.
	concatPaths := make([]string, 0, len(concatBatches))
	for p := range concatBatches {
		concatPaths = append(concatPaths, p)
	}
	sort.Strings(concatPaths)

	for _, p := range concatPaths {
		entries := concatBatches[p]
		if a.DryRun {
			a.Info(fmt.Sprintf("[dry-run] would regenerate concat %s with %d entries", p, len(entries)))
			continue
		}
		changed, err := RegenerateConcat(p, entries)
		if err != nil {
			a.Warn(fmt.Sprintf("concat regen failed for %s: %v", p, err))
			continue
		}
		if changed {
			a.Info(fmt.Sprintf("regenerated %s (%d entries)", p, len(entries)))
		} else {
			a.Info(fmt.Sprintf("%s already current (%d entries)", p, len(entries)))
		}
	}

	// Merge .agents/hooks/ fragments into .claude/settings.json
	// (SPEC-004 Part C). This runs after the per-artifact loop
	// because hooks are batch-processed, not one-at-a-time.
	if hasClaudeTarget {
		hooksDir := filepath.Join(root, "hooks")
		settingsPath := filepath.Join(parent, ".claude", "settings.json")
		statePath := filepath.Join(root, ".sync", "claude-hooks-state.json")
		if a.DryRun {
			if _, err := os.Stat(hooksDir); err == nil {
				a.Info(fmt.Sprintf("[dry-run] would merge hooks into %s", settingsPath))
			}
		} else {
			n, err := a.MergeHooks(hooksDir, settingsPath, statePath)
			if err != nil {
				a.Warn(fmt.Sprintf("hooks merge failed: %v", err))
			} else if n > 0 {
				a.Info(fmt.Sprintf("merged %d hook(s) into %s", n, settingsPath))
			}
		}
	}

	if !a.DryRun {
		a.Info("global sync complete")
	}
	return nil
}

// resolveSyncTools filters the Tools registry by the --targets list
// (if any), mapping each name through ResolveTool so aliases work.
// Unknown names are reported as warnings; the function returns
// successfully even when some targets are unknown, because partial
// progress is more useful than failing the whole sync.
func (a *App) resolveSyncTools(targets []string) ([]Tool, error) {
	if len(targets) == 0 {
		// Default: all tools with a global mapping.
		var out []Tool
		for _, t := range Tools {
			if t.HasScope(ScopeGlobal) {
				out = append(out, t)
			}
		}
		return out, nil
	}

	var out []Tool
	seen := map[string]bool{}
	for _, name := range targets {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			continue
		}
		tool, ok := ResolveTool(name)
		if !ok {
			a.Warn(fmt.Sprintf("unknown target %q; skipping", name))
			continue
		}
		if !tool.HasScope(ScopeGlobal) {
			a.Warn(fmt.Sprintf("target %q has no global scope; skipping", name))
			continue
		}
		if seen[tool.ID] {
			continue
		}
		seen[tool.ID] = true
		out = append(out, tool)
	}
	return out, nil
}

// applySymlinkDestination creates or repairs the symlink at dest.Path
// pointing at the artifact's content source. Handles three cases:
//
//   - Nothing at dest: create the symlink (after ensuring its parent
//     dir exists).
//   - Symlink already at dest: if it points at the right target,
//     no-op (logged once at a higher level for brevity). Otherwise
//     replace.
//   - Non-symlink at dest: skip with warning unless App.Force is
//     set, in which case the existing file is renamed to a
//     `.replaced-by-sync-agents-<timestamp>` sibling and the symlink
//     is placed.
//
// Symlinks are absolute paths (SPEC-002 §Global sync — symlink
// semantics). Relative would be brittle because the global tree's
// per-tool dirs are at different depths from ~/.agents/.
func (a *App) applySymlinkDestination(toolID string, art Artifact, dest Destination) error {
	target := symlinkTarget(art)

	if a.DryRun {
		a.Info(fmt.Sprintf("[dry-run] [%s] would link %s -> %s", toolID, dest.Path, target))
		return nil
	}

	existing, lerr := os.Lstat(dest.Path)
	if lerr == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			// Existing symlink. Check whether it points at the
			// right target; if so, no-op.
			current, _ := os.Readlink(dest.Path)
			if current == target {
				return nil
			}
			// Drifted symlink — repair.
			if err := os.Remove(dest.Path); err != nil {
				return err
			}
			a.Info(fmt.Sprintf("[%s] repair: %s now -> %s", toolID, dest.Path, target))
		} else {
			// Non-symlink in the way. Without --force, refuse.
			if !a.Force {
				return fmt.Errorf("non-symlink at %s; pass --force to overwrite", dest.Path)
			}
			// With --force, rename the conflicting file/dir to a
			// side path so it's recoverable.
			backup := fmt.Sprintf("%s.replaced-by-sync-agents", dest.Path)
			if err := os.Rename(dest.Path, backup); err != nil {
				return err
			}
			a.Warn(fmt.Sprintf("[%s] moved existing %s to %s", toolID, dest.Path, backup))
		}
	} else if !os.IsNotExist(lerr) {
		return lerr
	}

	if err := os.MkdirAll(filepath.Dir(dest.Path), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(target, dest.Path); err != nil {
		return err
	}
	return nil
}

// symlinkTarget returns the path a tool's per-artifact symlink
// should point at. For rules and workflows this is the .md file
// itself; for skills it's the SKILL.md inside the source dir,
// because Claude reads ~/.claude/skills/<name>/SKILL.md as the
// metadata file even when the destination is structured as a dir.
//
// We do NOT include the skill's supporting files in the symlink
// chain — they remain under ~/.agents/skills/<name>/ and are not
// directly referenced by per-tool dirs. Tools that need them have
// to walk the symlink target's parent dir, which is fine.
func symlinkTarget(art Artifact) string {
	if art.Type == ArtifactSkill {
		return filepath.Join(art.SourcePath, "SKILL.md")
	}
	return art.SourcePath
}

// concatSourcePath returns the path RegenerateConcat should read for
// this artifact's body. Same shape as symlinkTarget — the skill's
// content is in SKILL.md.
func concatSourcePath(art Artifact) string {
	if art.Type == ArtifactSkill {
		return filepath.Join(art.SourcePath, "SKILL.md")
	}
	return art.SourcePath
}

// Artifact represents one rule, skill, or workflow under the
// canonical .agents/ tree. The orchestration layer walks the tree
// once and produces a slice of these.
type Artifact struct {
	Type ArtifactType

	// Name is the artifact's identifier — file basename without
	// .md for rules/workflows, directory name for skills.
	Name string

	// SourcePath is the absolute path of the artifact's canonical
	// location: the .md file for rules/workflows, the directory for
	// skills.
	SourcePath string
}

// DiscoverArtifacts walks rootAgentsDir/{rules,skills,workflows}
// and returns every artifact it finds.
//
// Rules and workflows: every .md file in their respective subdir is
// an artifact. Hidden files (starting with `.`) are ignored.
//
// Skills: every subdirectory of skills/ that contains a SKILL.md
// is an artifact. Other subdirs are skipped with no warning
// (legitimate scratch dirs etc.).
//
// DiscoverArtifacts is intentionally non-recursive past the first
// level of each bucket; flatter layouts are easier to reason about
// and SPEC-002's routing doesn't model nested sub-buckets.
func DiscoverArtifacts(rootAgentsDir string) ([]Artifact, error) {
	var out []Artifact

	for _, b := range Buckets {
		dir := filepath.Join(rootAgentsDir, b.Dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			abs := filepath.Join(dir, e.Name())
			if b.DirPerArtifact {
				if !e.IsDir() {
					continue
				}
				if _, err := os.Stat(filepath.Join(abs, "SKILL.md")); err != nil {
					continue
				}
				out = append(out, Artifact{
					Type:       b.Artifact,
					Name:       e.Name(),
					SourcePath: abs,
				})
			} else {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				out = append(out, Artifact{
					Type:       b.Artifact,
					Name:       strings.TrimSuffix(e.Name(), ".md"),
					SourcePath: abs,
				})
			}
		}
	}

	// Sort for deterministic processing order. The Tools loop is
	// also deterministic (registry order), so the full sync is
	// reproducible.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
