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
// exists so hand-authored content has a home that survives: the
// source of truth is this file, and AGENTS.md stays entirely
// machine-generated. See SPEC-007.
const PreambleFileName = "AGENTS.preamble.md"

// PreambleMarker is written into the generated index immediately
// before injected preamble content.
//
// Its purpose is to make the preamble's presence detectable in the
// output, so a later run that would drop it can refuse rather than
// silently strip. AGENTS.md is regenerated wholesale, the preamble is
// frequently the bulk of it, and the failure is invisible: the
// command succeeds and the file shrinks by a few hundred lines. See
// preambleWouldBeDropped.
const PreambleMarker = "<!-- sync-agents:preamble -->"

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
// generated on this machine, which is the point of the feature.
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

// preambleWouldBeDropped reports whether writing newContent over the
// file at path would remove preamble content that is currently there.
//
// This catches the case where the preamble file cannot be read at
// index time — a fresh clone that has not got it, a --global-root
// pointing at the wrong tree, a rename — and the run would therefore
// regenerate an index without it. ReadPreamble treats an unreadable
// preamble as simply absent, which is correct for a repository that
// never had one and wrong for one that did.
//
// It cannot protect against a build with no preamble support at all,
// since such a build does not run this check. That gap closes by
// releasing, not by code here.
func preambleWouldBeDropped(path, newContent string) bool {
	if strings.Contains(newContent, PreambleMarker) {
		return false
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(existing), PreambleMarker)
}
