package agent

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
)

// DestinationState classifies the filesystem state of one per-artifact
// destination from a previously-run (or never-run) `global sync`.
//
// The four-state vocabulary maps to SPEC-002's "[synced] / [missing] /
// [not a symlink]" output, plus a [drifted] state for symlinks whose
// target moved out from under them.
type DestinationState string

const (
	// StateSynced: a symlink exists at the destination and points at
	// the canonical artifact under ~/.agents/. No action needed.
	StateSynced DestinationState = "synced"

	// StateDrifted: a symlink exists but its target is different
	// from what a fresh sync would create. Re-running `global sync`
	// will repair it.
	StateDrifted DestinationState = "drifted"

	// StateNotSymlink: a regular file or directory, or a user-owned
	// link to a directory outside the global root, occupies the
	// destination. `global sync` will skip it unless --force is set.
	StateNotSymlink DestinationState = "not-a-symlink"

	// StateMissing: nothing exists at the destination. `global sync`
	// will create the symlink.
	StateMissing DestinationState = "missing"
)

// ConcatState classifies the filesystem state of a concat target
// (Windsurf memories, Copilot/Codex instructions.md). Concat targets
// are content-compared rather than identity-compared, so the states
// differ from per-symlink DestinationState.
type ConcatState string

const (
	// ConcatStateOK: the concat file exists, carries the
	// sync-agents banner, and its content matches what a fresh
	// regeneration would produce.
	ConcatStateOK ConcatState = "ok"

	// ConcatStateStale: the file exists but its content differs
	// from what regeneration would produce. Running `global sync`
	// will rewrite it.
	ConcatStateStale ConcatState = "stale"

	// ConcatStateMissing: no file at the destination. `global sync`
	// will create it.
	ConcatStateMissing ConcatState = "missing"

	// ConcatStateForeign: a file exists at the destination but
	// lacks the sync-agents banner — almost certainly a user-owned
	// file we should not touch. `global sync` will overwrite it
	// (concat targets are sync-agents-owned by design); `global
	// clean` will leave it alone.
	ConcatStateForeign ConcatState = "foreign"
)

// StatusEntry is one row of `global status` output. Each entry maps
// to a single (tool, artifact) destination OR a single concat target.
type StatusEntry struct {
	// Tool is the destination tool ID (claude, codeium, …).
	Tool string

	// ArtifactType is the source bucket; empty for concat-only rows
	// where multiple buckets contribute to the same file.
	ArtifactType ArtifactType

	// ArtifactName is the source artifact's name; empty for concat
	// rows.
	ArtifactName string

	// DestinationPath is the absolute path being reported on.
	DestinationPath string

	// State is exactly one of {synced, drifted, not-a-symlink,
	// missing} for symlink destinations, or one of {ok, stale,
	// missing, foreign} for concat destinations. Stored as string
	// so callers don't have to type-switch when rendering.
	State string

	// IsConcat is true for concat-target rows so callers can
	// distinguish them in output.
	IsConcat bool

	// Detail is an optional human-readable note (e.g. the actual
	// vs expected symlink target for drifted state).
	Detail string
}

// GlobalStatusOpts holds the options for CmdGlobalStatus. The zero
// value is "all registered tools, plain text output."
type GlobalStatusOpts struct {
	// Targets, when non-empty, filters which tools are reported on.
	// Aliases honored via ResolveTool.
	Targets []string
}

// CmdGlobalStatus inspects the user's global per-tool directories
// and reports the state of every destination that `global sync`
// would manage.
//
// The function is read-only: no filesystem writes occur. It walks
// the same artifact set DiscoverArtifacts produces and resolves the
// same destinations TargetDestination would, then stats each one.
//
// Output: one line per (tool, artifact) destination plus one line
// per concat target. Format:
//
//	[STATE] tool/typ/name -> path  (detail)
//	[concat STATE] path  (N entries)
//
// Concat targets are aggregated (one line per unique concat path,
// not one per contributing artifact). Empty global root produces
// a clear error pointing at `global init`.
//
// See SPEC-002 §Requirement: Global status and
// docs/commands/global-status.md for the user-facing contract.
func (a *App) CmdGlobalStatus(opts GlobalStatusOpts) error {
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

	artifacts, err := DiscoverArtifacts(root)
	if err != nil {
		return err
	}

	entries, concatRows := computeStatus(artifacts, tools, root, parent)

	a.Info(fmt.Sprintf("global status (%d artifacts, %d tool(s)):", len(artifacts), len(tools)))

	for _, e := range entries {
		a.printStatusEntry(e)
	}
	for _, e := range concatRows {
		a.printStatusEntry(e)
	}
	return nil
}

// computeStatus is the pure logic of `global status`: given the
// discovered artifacts and tool set, return the per-destination
// entries and the aggregated concat rows.
//
// Split out from CmdGlobalStatus so tests can exercise the state
// classification without going through the App + filesystem-write
// surface.
func computeStatus(artifacts []Artifact, tools []Tool, root, parent string) (perDestination []StatusEntry, concatRows []StatusEntry) {
	// Track concat targets and the entries that contribute to them
	// so we can both classify their state AND show the contributor
	// count.
	concatBatches := map[string][]ConcatEntry{}
	concatTool := map[string]string{} // path → tool ID for display

	for _, art := range artifacts {
		sem, err := ResolveSemantic(art.SourcePath, art.Type)
		if err != nil {
			// Treat a semantic error as "we don't know how to
			// route this" — surface as a synthetic row.
			perDestination = append(perDestination, StatusEntry{
				ArtifactType:    art.Type,
				ArtifactName:    art.Name,
				DestinationPath: art.SourcePath,
				State:           "frontmatter-error",
				Detail:          err.Error(),
			})
			continue
		}
		for _, tool := range tools {
			dest := TargetDestination(tool, art.Type, art.Name, sem, art.SourcePath, parent)
			switch dest.Strategy {
			case StrategySkip:
				perDestination = append(perDestination, StatusEntry{
					Tool:         tool.ID,
					ArtifactType: art.Type,
					ArtifactName: art.Name,
					State:        "skipped",
					Detail:       dest.SkipReason,
				})
			case StrategySymlink:
				state, detail := classifySymlinkDestination(dest.Path, dest.Target)
				if state == StateDrifted && isUserDirLink(dest.Path, root) {
					target, _ := os.Readlink(dest.Path)
					state, detail = StateNotSymlink, fmt.Sprintf("user-owned link to %s; sync replaces it only with --force", target)
				}
				perDestination = append(perDestination, StatusEntry{
					Tool:            tool.ID,
					ArtifactType:    art.Type,
					ArtifactName:    art.Name,
					DestinationPath: dest.Path,
					State:           string(state),
					Detail:          detail,
				})
			case StrategyConcat:
				concatBatches[dest.Path] = append(concatBatches[dest.Path], ConcatEntry{
					Name:       art.Name,
					SourcePath: concatSourcePath(art),
				})
				concatTool[dest.Path] = tool.ID
			}
		}
	}

	// Sort the per-destination rows for deterministic output.
	sort.Slice(perDestination, func(i, j int) bool {
		if perDestination[i].Tool != perDestination[j].Tool {
			return perDestination[i].Tool < perDestination[j].Tool
		}
		if perDestination[i].ArtifactType != perDestination[j].ArtifactType {
			return perDestination[i].ArtifactType < perDestination[j].ArtifactType
		}
		return perDestination[i].ArtifactName < perDestination[j].ArtifactName
	})

	// Concat rows: one per unique path, with the contributing-entry
	// count in Detail.
	concatPaths := make([]string, 0, len(concatBatches))
	for p := range concatBatches {
		concatPaths = append(concatPaths, p)
	}
	sort.Strings(concatPaths)

	for _, p := range concatPaths {
		entries := concatBatches[p]
		state := classifyConcatTarget(p, entries)
		concatRows = append(concatRows, StatusEntry{
			Tool:            concatTool[p],
			DestinationPath: p,
			State:           string(state),
			IsConcat:        true,
			Detail:          fmt.Sprintf("%d entries", len(entries)),
		})
	}

	return perDestination, concatRows
}

// classifySymlinkDestination inspects a symlink destination and
// returns its state.
func classifySymlinkDestination(destPath, wantTarget string) (DestinationState, string) {
	info, err := os.Lstat(destPath)
	if err != nil {
		if os.IsNotExist(err) {
			return StateMissing, ""
		}
		return StateNotSymlink, err.Error()
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if isLegacyClaudeSkillDir(destPath, wantTarget) {
			return StateDrifted, "legacy skill dir linking only SKILL.md; sync replaces it with a link to the skill directory"
		}
		return StateNotSymlink, ""
	}
	current, err := os.Readlink(destPath)
	if err != nil {
		return StateDrifted, "readlink failed: " + err.Error()
	}
	if current != wantTarget {
		return StateDrifted, fmt.Sprintf("points at %s, want %s", current, wantTarget)
	}
	return StateSynced, ""
}

// classifyConcatTarget inspects a concat target and returns its
// state, comparing existing content against what RegenerateConcat
// would produce.
//
// We avoid actually calling RegenerateConcat (it writes to disk).
// Instead we read the existing content, compute the expected content
// in-memory using the same helpers RegenerateConcat uses, and
// compare bytes.
func classifyConcatTarget(path string, entries []ConcatEntry) ConcatState {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ConcatStateMissing
		}
		return ConcatStateMissing
	}
	if !bytes.HasPrefix(existing, []byte("<!--\nGenerated by sync-agents")) {
		return ConcatStateForeign
	}
	// Build the expected content the same way RegenerateConcat does.
	want, err := buildConcatContent(entries)
	if err != nil {
		// If we can't read a source artifact, we can't say for sure;
		// treat as stale so the user runs sync to re-attempt.
		return ConcatStateStale
	}
	if bytes.Equal(existing, want) {
		return ConcatStateOK
	}
	return ConcatStateStale
}

// buildConcatContent runs the regeneration build step *without*
// touching the filesystem. Used by classifyConcatTarget to compare
// against the existing concat file.
//
// This is a small duplication of the build step inside
// RegenerateConcat; the alternative is to factor that step out and
// have RegenerateConcat call it. Either works; the duplication is
// trivially small and isolated by tests.
func buildConcatContent(entries []ConcatEntry) ([]byte, error) {
	sorted := make([]ConcatEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var buf bytes.Buffer
	buf.WriteString(ConcatBanner)
	for _, e := range sorted {
		body, err := readArtifactBody(e.SourcePath)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&buf, "## %s\n\n", e.Name)
		buf.Write(body)
		if !bytes.HasSuffix(bytes.TrimRight(buf.Bytes(), " \t"), []byte("\n")) {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// printStatusEntry formats one StatusEntry as a single text line. The
// format is intentionally regex-friendly: a state bracketed at the
// start, then the tool/type/name (when present), then `->` and the
// path, optionally a parenthetical detail.
func (a *App) printStatusEntry(e StatusEntry) {
	var sb strings.Builder
	sb.WriteString("[")
	if e.IsConcat {
		sb.WriteString("concat ")
	}
	sb.WriteString(e.State)
	sb.WriteString("] ")

	if e.IsConcat {
		sb.WriteString(e.DestinationPath)
	} else {
		if e.Tool != "" {
			sb.WriteString(e.Tool)
			sb.WriteString("/")
		}
		if e.ArtifactType != "" {
			sb.WriteString(string(e.ArtifactType))
			sb.WriteString("/")
		}
		sb.WriteString(e.ArtifactName)
		if e.DestinationPath != "" {
			sb.WriteString(" -> ")
			sb.WriteString(e.DestinationPath)
		}
	}

	if e.Detail != "" {
		sb.WriteString("  (")
		sb.WriteString(e.Detail)
		sb.WriteString(")")
	}
	fmt.Fprintln(a.Stdout, sb.String())
}
