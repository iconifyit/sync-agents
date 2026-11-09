package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Managed-import block markers. These delimit the region in CLAUDE.md
// (or AGENTS.md when it's the Claude target) that sync-agents fully
// regenerates on every sync. Content between start and end —
// including any blank lines — is replaced wholesale each write; the
// markers themselves are preserved. Everything outside the markers
// is left verbatim, so users can keep their own frontmatter, prose,
// or other sections safely alongside the managed imports.
//
// The naming scheme mirrors other preserved-section markers in this
// project (e.g. the ## Inherits block in AGENTS.md).
const (
	ManagedImportBlockStart = "<!-- sync-agents:claude-imports:start -->"
	ManagedImportBlockEnd   = "<!-- sync-agents:claude-imports:end -->"
)

// managedImportBanner is a short comment that explains where the
// block comes from. It sits inside the managed region (after the
// start marker) so users who open CLAUDE.md see the origin at a
// glance without having to read sync-agents docs.
const managedImportBanner = "<!-- managed by sync-agents; do not edit between the markers -->"

// RegenerateClaudeImports writes — or refreshes — the managed
// @-import block at claudeMDPath. claudeMDPath is the absolute path
// to the file that should carry the block, typically either
// ~/.claude/CLAUDE.md (global scope) or <project>/AGENTS.md (local
// scope, since CLAUDE.md is a symlink there).
//
// importPaths are the @-import lines to emit, one per path. Each
// entry is emitted verbatim as `@<path>` on its own line — callers
// pass fully-formed paths (absolute for global scope, project-
// relative for local scope) so this function doesn't have to know
// about either convention.
//
// Sorting: the input slice is sorted before writing so the output
// is deterministic regardless of the caller's discovery order. Empty
// input produces an empty block (only the markers + banner).
//
// Idempotency: if the resulting file content byte-equals the
// existing file content, RegenerateClaudeImports does not perform
// the write. This preserves mtime so `stat`-based change detectors
// don't fire on every re-run. Callers should log `changed=true` so
// users can see that sync actually did something meaningful.
//
// Atomicity: writes use a sibling tmp + rename so a partial write
// never lands at claudeMDPath.
//
// Returns (changed, error):
//   - changed=true: file was written (new file, new content, or
//     markers added for the first time).
//   - changed=false: file already had byte-identical content.
//   - error: non-nil only on filesystem failure; callers should log
//     and move on (a failed imports write shouldn't abort the whole
//     sync).
func RegenerateClaudeImports(claudeMDPath string, importPaths []string) (bool, error) {
	sorted := make([]string, len(importPaths))
	copy(sorted, importPaths)
	sort.Strings(sorted)

	var buf bytes.Buffer
	buf.WriteString(ManagedImportBlockStart)
	buf.WriteString("\n")
	buf.WriteString(managedImportBanner)
	buf.WriteString("\n")
	for _, p := range sorted {
		buf.WriteString("@")
		buf.WriteString(p)
		buf.WriteString("\n")
	}
	buf.WriteString(ManagedImportBlockEnd)
	buf.WriteString("\n")

	newBlock := buf.String()

	existing, readErr := os.ReadFile(claudeMDPath)
	var out string
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return false, readErr
		}
		// File doesn't exist yet — the block IS the whole file.
		// Ensure the parent dir exists before write.
		out = newBlock
	} else {
		out = replaceManagedBlock(string(existing), newBlock)
	}

	// Idempotency: skip write when the file is already content-
	// identical to what we would produce.
	if readErr == nil && string(existing) == out {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(claudeMDPath), 0o755); err != nil {
		return false, err
	}

	tmp := claudeMDPath + ".sync-agents-tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, claudeMDPath); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// replaceManagedBlock replaces the content between the managed-block
// markers with newBlock. If the markers are missing, the new block
// is appended to the existing content (with one blank line separator
// when the original ends without a trailing newline). If the markers
// are present, everything between them — including the lines on the
// same line as the markers — is replaced.
//
// Edge cases handled:
//   - Start marker present, end marker missing: append a fresh end-
//     terminated block (treat as if markers missing on the tail).
//     Practically shouldn't happen; graceful fallback.
//   - Markers present at the very start of the file: replaced in
//     place; leading content before the start marker is empty.
//   - Multiple marker pairs: only the first pair is replaced; later
//     pairs are left verbatim (a pathological case we don't need to
//     defend against).
func replaceManagedBlock(existing, newBlock string) string {
	return replaceBlockBetweenMarkers(existing, newBlock, ManagedImportBlockStart, ManagedImportBlockEnd)
}

// replaceBlockBetweenMarkers is replaceManagedBlock generalized over
// the marker pair, so more than one managed region can coexist in the
// same file. CLAUDE.md now carries two: the AGENTS.md mirror and the
// `@`-import block. Behavior for the import block is unchanged —
// replaceManagedBlock simply passes its own markers through.
func replaceBlockBetweenMarkers(existing, newBlock, startMarker, endMarker string) string {
	startIdx := strings.Index(existing, startMarker)
	if startIdx < 0 {
		// No existing block. Append — with a separator blank line
		// if the existing content doesn't already end with one.
		if len(existing) == 0 {
			return newBlock
		}
		sep := ""
		if !strings.HasSuffix(existing, "\n") {
			sep = "\n"
		}
		return existing + sep + "\n" + newBlock
	}

	endIdx := strings.Index(existing[startIdx:], endMarker)
	if endIdx < 0 {
		// Start present, end missing — malformed. Treat as if
		// start was not present: append the block.
		sep := ""
		if !strings.HasSuffix(existing, "\n") {
			sep = "\n"
		}
		return existing + sep + "\n" + newBlock
	}
	// endIdx is relative to startIdx; make it absolute.
	endIdx += startIdx
	endFull := endIdx + len(endMarker)

	// Eat the trailing newline after the end marker (if any) so the
	// replaced block owns its own trailing newline.
	if endFull < len(existing) && existing[endFull] == '\n' {
		endFull++
	}

	var out strings.Builder
	out.WriteString(existing[:startIdx])
	out.WriteString(newBlock)
	if endFull < len(existing) {
		out.WriteString(existing[endFull:])
	}
	return out.String()
}

// CollectClaudeRuleImportPaths walks the passive-rule destinations
// that a global sync just produced for Claude and returns the
// absolute `@`-import paths to emit in the managed block.
//
// Callers pass the global root parent (same value used to resolve
// the `.claude/` dir in destination.go) plus the set of artifact
// (name, sem, typ) tuples that routed to Claude at StrategySymlink
// with Passive semantic. We don't scan the filesystem here — we
// trust the sync's routing decisions to be authoritative.
//
// The returned paths are absolute, pointing at the symlinked
// location `~/.claude/rules/<name>.md` — not the underlying
// `~/.agents/...` source — because that's the tool-native surface
// Claude knows about. Since @-import follows symlinks, both would
// work, but `.claude/rules/...` matches what a user would write by
// hand and makes the block self-explanatory.
//
// Only Passive rules + Passive workflows qualify. Invocable
// artifacts (commands/, skills/) don't need the block because Claude
// already auto-discovers them at their destinations.
func CollectClaudeRuleImportPaths(parent string, arts []ClaudeRoutedArtifact) []string {
	claudeDir := filepath.Join(parent, ".claude")
	var out []string
	for _, art := range arts {
		if art.Semantic != Passive {
			continue
		}
		if art.Type != ArtifactRule && art.Type != ArtifactWorkflow {
			continue
		}
		out = append(out, filepath.Join(claudeDir, "rules", art.Name+".md"))
	}
	return out
}

// ClaudeRoutedArtifact is the subset of artifact metadata needed to
// decide whether a global-sync routing contributes to the Claude
// @-import block. The orchestration layer builds these as it walks
// the artifacts.
//
// We intentionally don't reuse Artifact (which carries SourcePath)
// because the imports block cares about the routed name/semantic,
// not the filesystem source.
type ClaudeRoutedArtifact struct {
	Type     ArtifactType
	Name     string
	Semantic Semantic
}

// FormatManagedImportBlockForTest exposes replaceManagedBlock's
// behavior for tests that want to assert on the exact block output
// without going through filesystem writes. Production code calls
// RegenerateClaudeImports directly.
var FormatManagedImportBlockForTest = func(importPaths []string) string {
	sorted := make([]string, len(importPaths))
	copy(sorted, importPaths)
	sort.Strings(sorted)
	var buf bytes.Buffer
	buf.WriteString(ManagedImportBlockStart)
	buf.WriteString("\n")
	buf.WriteString(managedImportBanner)
	buf.WriteString("\n")
	for _, p := range sorted {
		buf.WriteString("@")
		buf.WriteString(p)
		buf.WriteString("\n")
	}
	buf.WriteString(ManagedImportBlockEnd)
	buf.WriteString("\n")
	return buf.String()
}

// managedBlockMarkerRegexp is used by tests to verify that a file
// contains a well-formed managed block. Exported via a test-only
// helper so test packages don't have to re-implement the regex.
var managedBlockMarkerRegexp = regexp.MustCompile(
	"(?s)" + regexp.QuoteMeta(ManagedImportBlockStart) +
		".*?" +
		regexp.QuoteMeta(ManagedImportBlockEnd),
)

// HasManagedImportBlock reports whether content contains a
// well-formed start...end marker pair.
func HasManagedImportBlock(content string) bool {
	return managedBlockMarkerRegexp.MatchString(content)
}

// ExtractManagedImports returns the @-import lines between the
// managed markers, with leading `@` stripped and blank lines
// dropped. Returns nil when no markers exist.
func ExtractManagedImports(content string) []string {
	startIdx := strings.Index(content, ManagedImportBlockStart)
	if startIdx < 0 {
		return nil
	}
	endIdx := strings.Index(content[startIdx:], ManagedImportBlockEnd)
	if endIdx < 0 {
		return nil
	}
	region := content[startIdx+len(ManagedImportBlockStart) : startIdx+endIdx]
	var out []string
	for _, line := range strings.Split(region, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			continue
		}
		out = append(out, strings.TrimPrefix(line, "@"))
	}
	return out
}

// ResolveClaudeMDPath returns the absolute path to the Claude
// project-level CLAUDE.md that should carry the managed @-import
// block. For local sync, CLAUDE.md is a symlink to AGENTS.md, so we
// write the block into AGENTS.md (the symlink target) — Claude
// follows the symlink when loading.
//
// Callers pass the project root. If AGENTS.md exists, its path is
// returned; otherwise we return <projectRoot>/CLAUDE.md (callers
// create one if needed at write time).
func ResolveClaudeMDPath(projectRoot string) string {
	agentsMD := filepath.Join(projectRoot, "AGENTS.md")
	if _, err := os.Stat(agentsMD); err == nil {
		return agentsMD
	}
	return filepath.Join(projectRoot, "CLAUDE.md")
}

// ManagedImportBlockForLocal returns the fully-formed @-import lines
// (without the surrounding markers) for a local sync. Each entry
// takes the form `@.claude/rules/<name>.md` — project-relative
// because AGENTS.md is checked into git and absolute paths would
// not be portable across developers.
//
// Only Passive artifacts (rules, workflows) qualify. The paths
// reference `.claude/rules/` (the Claude-visible surface in the
// project root) rather than `.agents/rules/` so the block is
// self-contained: every listed file is resolvable relative to the
// project root where CLAUDE.md lives.
func ManagedImportBlockForLocal(arts []ClaudeRoutedArtifact) []string {
	var out []string
	for _, art := range arts {
		if art.Semantic != Passive {
			continue
		}
		if art.Type != ArtifactRule && art.Type != ArtifactWorkflow {
			continue
		}
		out = append(out, fmt.Sprintf(".claude/rules/%s.md", art.Name))
	}
	return out
}
