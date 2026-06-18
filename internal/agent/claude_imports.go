package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Claude reads `CLAUDE.md` automatically at session start, but it does
// NOT auto-scan `~/.claude/rules/*.md` and it does NOT follow markdown
// links inside `CLAUDE.md` / `AGENTS.md`. So passive rules placed at
// `~/.claude/rules/<name>.md` by the per-tool routing in destination.go
// never make it into Claude's loaded context — even though they're at
// the routing-correct destination.
//
// The mechanism Claude *does* honor is the `@`-import: a line of the
// form `@<absolute-path>` inside `CLAUDE.md` is inlined into context at
// load time. This file maintains a managed block of those imports —
// one per passive rule — inside `~/.claude/CLAUDE.md`, regenerated from
// scratch on every sync.
//
// The block is wrapped in HTML-comment markers that the regenerator can
// find and replace in-place. Content outside the markers is preserved
// verbatim, so users (or `index`) can write their own surrounding text
// without conflict — same preserved-region pattern as the `## Inherits`
// section in `AGENTS.md`.
//
// See sync-agents issue #46 for the upstream motivation and the probe
// that confirmed Claude's loader behavior.

// Markers used to wrap the managed `@`-imports block inside CLAUDE.md.
// They are HTML comments so they are invisible when CLAUDE.md is
// rendered as markdown, and unique enough that a regex match is safe.
const (
	ClaudeImportsBlockStart = "<!-- sync-agents:claude-rules:start -->"
	ClaudeImportsBlockEnd   = "<!-- sync-agents:claude-rules:end -->"
)

// claudeImportsBlockRE matches the managed block plus any surrounding
// blank lines so replacement and removal can collapse whitespace
// cleanly. `(?s)` makes `.` match newlines.
var claudeImportsBlockRE = regexp.MustCompile(
	`(?s)\n*` + regexp.QuoteMeta(ClaudeImportsBlockStart) +
		`.*?` + regexp.QuoteMeta(ClaudeImportsBlockEnd) + `\n*`,
)

// BuildClaudeImportBlock returns the canonical block content for the
// given list of `@`-import paths. The paths are sorted alphabetically
// so block output is deterministic across runs regardless of the order
// the sync loop happens to discover them.
//
// An empty paths slice produces a block containing only the markers
// (no `@`-lines). That is intentional: the markers stay so a future
// sync that finds rules has a clean injection point, and the empty
// block harms nothing if rendered.
func BuildClaudeImportBlock(paths []string) string {
	sorted := make([]string, len(paths))
	copy(sorted, paths)
	sort.Strings(sorted)

	var b strings.Builder
	b.WriteString(ClaudeImportsBlockStart)
	for _, p := range sorted {
		b.WriteByte('\n')
		b.WriteByte('@')
		b.WriteString(p)
	}
	b.WriteByte('\n')
	b.WriteString(ClaudeImportsBlockEnd)
	return b.String()
}

// UpsertClaudeImportBlock returns `existing` with the managed Claude
// imports block replaced (if present) or appended (if absent). It is
// a pure function — no filesystem writes.
//
// When the block is absent and `existing` has prior content, a blank
// line separates the new block from that content. When `existing` is
// empty, the block stands alone with a trailing newline.
//
// Content outside the managed markers is preserved byte-for-byte.
func UpsertClaudeImportBlock(existing, block string) string {
	cleaned := RemoveClaudeImportBlock(existing)
	cleaned = strings.TrimRight(cleaned, "\n\t \r")
	if cleaned == "" {
		return block + "\n"
	}
	return cleaned + "\n\n" + block + "\n"
}

// RemoveClaudeImportBlock returns `existing` with the managed Claude
// imports block stripped. Surrounding blank lines are normalized so
// the join point between the "before block" and "after block"
// content has exactly one blank line separator (matching standard
// markdown paragraph spacing).
//
// If no block is present, `existing` is returned unchanged. Pure
// function — no filesystem writes.
func RemoveClaudeImportBlock(existing string) string {
	loc := claudeImportsBlockRE.FindStringIndex(existing)
	if loc == nil {
		return existing
	}
	before := existing[:loc[0]]
	after := existing[loc[1]:]
	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after
	}
}

// WriteClaudeImportsBlock idempotently writes the managed `@`-imports
// block into the CLAUDE.md at `claudeMdPath`. The file is created
// (with parent directories) if missing. Returns `(changed, err)` where
// `changed` reports whether the on-disk byte content actually
// differed from what we wrote — letting the caller log
// "regenerated" vs "already current".
//
// The block is regenerated from scratch each call; any prior managed
// block is replaced.
func WriteClaudeImportsBlock(claudeMdPath string, paths []string) (bool, error) {
	block := BuildClaudeImportBlock(paths)
	var existing string
	data, err := os.ReadFile(claudeMdPath)
	switch {
	case err == nil:
		existing = string(data)
	case os.IsNotExist(err):
		existing = ""
	default:
		return false, err
	}
	next := UpsertClaudeImportBlock(existing, block)
	if next == existing {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(claudeMdPath), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(claudeMdPath, []byte(next), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
