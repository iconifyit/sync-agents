package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// PreambleFileName is the hand-authored markdown file that
// `sync-agents index` injects into the generated AGENTS.md.
//
// AGENTS.md is regenerated wholesale on every index run, so anything
// typed directly into it is destroyed without warning. The preamble
// exists so that hand-authored content has a home that survives:
// the source of truth is this file, and AGENTS.md stays entirely
// machine-generated. See SPEC-004.
const PreambleFileName = "AGENTS.preamble.md"

// ResolvePreamblePath returns the absolute path of the global
// preamble file.
//
// Resolution goes through ResolveGlobalRoot, so --global-root and
// $SYNC_AGENTS_GLOBAL_ROOT are honored and tests can point it at a
// t.TempDir without touching the real $HOME.
//
// The preamble is deliberately global-only: a file of this name in a
// project's local .agents/ directory is ignored. One copy in the
// global tree propagates to every project whose AGENTS.md is
// generated on this machine, which is the whole point of the feature.
func (a *App) ResolvePreamblePath() string {
	return filepath.Join(a.ResolveGlobalRoot(), PreambleFileName)
}

// ReadPreamble returns the preamble's contents and whether one is
// present with meaningful content.
//
// A missing preamble is not an error. The feature is opt-in, and
// callers must generate AGENTS.md exactly as they would have without
// it — an unreadable or absent file simply yields ("", false).
//
// A file that is empty or holds only whitespace is treated as absent,
// so an accidentally-blanked preamble does not inject a stray gap
// into the generated index.
//
// Content is returned verbatim except for trailing whitespace, which
// is trimmed so the caller alone controls the blank-line spacing
// between the preamble and the sections after it. Nothing else is
// escaped, reflowed, or rewritten: headings, lists, code fences, and
// HTML comments all pass through untouched.
func (a *App) ReadPreamble() (string, bool) {
	data, err := os.ReadFile(a.ResolvePreamblePath())
	if err != nil {
		return "", false
	}
	content := strings.TrimRight(string(data), " \t\r\n")
	if strings.TrimSpace(content) == "" {
		return "", false
	}
	return content, true
}
