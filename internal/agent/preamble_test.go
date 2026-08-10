package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The preamble content used across these tests is deliberately
// realistic rather than symbolic: a real engineering-principles
// document is what this feature exists to carry, and its markdown
// (headings, nested lists, a code fence, an HTML comment) is exactly
// the content most likely to be mangled by a naive injector.
const testPreamble = `# Engineering Principles

These principles define how decisions are made.

## 1. Safety & Irreversibility

- No destructive or irreversible actions without explicit permission.
- When uncertain, choose the safest option or ask.

## 2. Verification

Verify before declaring success. For example:

` + "```sh" + `
npm test && npm run lint
` + "```" + `

<!-- reviewed 2026-08-10 -->`

// newPreambleTestApp returns an App whose ProjectRoot and GlobalRoot
// both live under a fresh t.TempDir, so nothing touches the real
// $HOME or the developer's own .agents tree. A minimal .agents/
// skeleton is created in the project so generateAgentsMD has
// something to index.
func newPreambleTestApp(t *testing.T) (*App, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	stdout := &bytes.Buffer{}
	projectRoot := filepath.Join(root, "project")

	// Seed every registered bucket, not just the InInit ones and not a
	// hardcoded list. generateAgentsMD warns about any bucket dir it
	// cannot stat — including plans/ and specs/, which `init` does not
	// create — and that noise would drown the assertion that the
	// preamble itself stays silent when absent. Driving this off the
	// registry also keeps the fixture correct as buckets are added
	// upstream.
	for _, bucket := range Buckets {
		if err := os.MkdirAll(filepath.Join(projectRoot, ".agents", bucket.Dir), 0755); err != nil {
			t.Fatalf("seeding .agents/%s: %v", bucket.Dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".agents"), 0755); err != nil {
		t.Fatalf("seeding global root: %v", err)
	}

	return &App{
		ProjectRoot: projectRoot,
		GlobalRoot:  filepath.Join(root, ".agents"),
		Stdout:      stdout,
		Stderr:      &bytes.Buffer{},
	}, stdout
}

// writePreamble seeds the global preamble file with the given body.
func writePreamble(t *testing.T, a *App, body string) {
	t.Helper()
	if err := os.WriteFile(a.ResolvePreamblePath(), []byte(body), 0644); err != nil {
		t.Fatalf("writing preamble: %v", err)
	}
}

// readAgentsMD returns the generated AGENTS.md as a string.
func readAgentsMD(t *testing.T, a *App) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(a.ProjectRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	return string(data)
}

// Scenario: Preamble present — content is injected, positioned after
// the header and before the generated index sections, and the index
// sections themselves are untouched.
func TestGenerateAgentsMD_InjectsPreamble(t *testing.T) {
	a, _ := newPreambleTestApp(t)
	writePreamble(t, a, testPreamble)

	a.generateAgentsMD()
	got := readAgentsMD(t, a)

	if !strings.Contains(got, "# Engineering Principles") {
		t.Fatalf("preamble heading missing from AGENTS.md:\n%s", got)
	}

	headerIdx := strings.Index(got, "This file indexes all rules")
	preambleIdx := strings.Index(got, "# Engineering Principles")
	rulesIdx := strings.Index(got, "## Rules")

	if !(headerIdx < preambleIdx && preambleIdx < rulesIdx) {
		t.Errorf("preamble misplaced: header=%d preamble=%d rules=%d", headerIdx, preambleIdx, rulesIdx)
	}
	for _, section := range []string{"## Rules", "## Skills", "## Workflows"} {
		if !strings.Contains(got, section) {
			t.Errorf("generated index lost section %q", section)
		}
	}
}

// Scenario: Preamble content is copied verbatim — no escaping,
// reflowing, or heading-level rewriting.
func TestGenerateAgentsMD_PreambleIsVerbatim(t *testing.T) {
	a, _ := newPreambleTestApp(t)
	writePreamble(t, a, testPreamble)

	a.generateAgentsMD()

	if !strings.Contains(readAgentsMD(t, a), testPreamble) {
		t.Error("preamble was altered during injection; expected byte-identical content")
	}
}

// Scenario: Preamble absent — AGENTS.md generates exactly as it would
// without the feature, and nothing is reported.
func TestGenerateAgentsMD_NoPreamble(t *testing.T) {
	a, stdout := newPreambleTestApp(t)

	a.generateAgentsMD()
	got := readAgentsMD(t, a)

	if !strings.Contains(got, "## Rules") {
		t.Fatalf("index sections missing:\n%s", got)
	}
	// The header must run straight into the index with no injected gap.
	if !strings.Contains(got, "defined in `.agents/`.\n\n## Rules") {
		t.Errorf("unexpected content between header and ## Rules:\n%s", got)
	}
	if stdout.String() != "" {
		t.Errorf("expected no output when preamble is absent, got %q", stdout.String())
	}
}

// Scenario: Preamble is empty — a zero-byte or whitespace-only file is
// treated as absent, introducing no stray blank sections.
func TestGenerateAgentsMD_EmptyPreambleTreatedAsAbsent(t *testing.T) {
	for name, body := range map[string]string{
		"zero bytes":      "",
		"whitespace only": "\n\n   \t\n",
	} {
		t.Run(name, func(t *testing.T) {
			a, _ := newPreambleTestApp(t)
			writePreamble(t, a, body)

			a.generateAgentsMD()

			if !strings.Contains(readAgentsMD(t, a), "defined in `.agents/`.\n\n## Rules") {
				t.Errorf("empty preamble introduced content between header and ## Rules")
			}
		})
	}
}

// Scenario: Injection is idempotent — running twice produces a
// byte-identical file with the preamble appearing exactly once. This
// is the property that makes the generated file safe to commit.
func TestGenerateAgentsMD_IdempotentWithPreamble(t *testing.T) {
	a, _ := newPreambleTestApp(t)
	writePreamble(t, a, testPreamble)

	a.generateAgentsMD()
	first := readAgentsMD(t, a)
	a.generateAgentsMD()
	second := readAgentsMD(t, a)

	if first != second {
		t.Error("second index run produced different bytes; generation is not idempotent")
	}
	if n := strings.Count(second, "# Engineering Principles"); n != 1 {
		t.Errorf("preamble appears %d times after two runs, want exactly 1", n)
	}
}

// Scenario: Project-local preamble is ignored — resolution is
// global-only, so a file of the same name in the project's .agents/
// must not be picked up.
func TestGenerateAgentsMD_IgnoresProjectLocalPreamble(t *testing.T) {
	a, _ := newPreambleTestApp(t)
	localPreamble := filepath.Join(a.ProjectRoot, ".agents", PreambleFileName)
	if err := os.WriteFile(localPreamble, []byte("# Project Local Principles"), 0644); err != nil {
		t.Fatalf("writing project-local preamble: %v", err)
	}

	a.generateAgentsMD()

	if strings.Contains(readAgentsMD(t, a), "Project Local Principles") {
		t.Error("project-local preamble was injected; resolution must be global-only")
	}
}

// Scenario: Global root override is honored — the preamble follows
// ResolveGlobalRoot, so --global-root points resolution at the
// overridden tree.
func TestResolvePreamblePath_HonorsGlobalRootOverride(t *testing.T) {
	a, _ := newPreambleTestApp(t)
	override := t.TempDir()
	a.GlobalRoot = override

	got := a.ResolvePreamblePath()
	want := filepath.Join(override, PreambleFileName)

	if got != want {
		t.Errorf("ResolvePreamblePath() = %q, want %q", got, want)
	}
}

// Scenario: Dry-run does not write. The pre-existing AGENTS.md is
// deliberately stale relative to the agents tree, so a write would be
// detectable rather than coincidentally identical.
func TestCmdIndex_DryRunDoesNotWrite(t *testing.T) {
	a, stdout := newPreambleTestApp(t)
	writePreamble(t, a, testPreamble)

	stale := "# stale content that a real run would replace\n"
	outfile := filepath.Join(a.ProjectRoot, "AGENTS.md")
	if err := os.WriteFile(outfile, []byte(stale), 0644); err != nil {
		t.Fatalf("seeding stale AGENTS.md: %v", err)
	}

	a.DryRun = true
	if err := a.CmdIndex(); err != nil {
		t.Fatalf("CmdIndex returned %v", err)
	}

	if got := readAgentsMD(t, a); got != stale {
		t.Errorf("dry run modified AGENTS.md:\ngot:  %q\nwant: %q", got, stale)
	}
	if strings.Contains(stdout.String(), "Regenerated AGENTS.md") {
		t.Errorf("dry run claimed it regenerated the file: %q", stdout.String())
	}
}

// Scenario: Dry-run reports the preamble it would inject.
func TestCmdIndex_DryRunReportsPreamble(t *testing.T) {
	a, stdout := newPreambleTestApp(t)
	writePreamble(t, a, testPreamble)
	a.DryRun = true

	if err := a.CmdIndex(); err != nil {
		t.Fatalf("CmdIndex returned %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "would inject preamble") {
		t.Errorf("dry run did not report the preamble it would inject: %q", out)
	}
	if !strings.Contains(out, a.ResolvePreamblePath()) {
		t.Errorf("dry run did not name the preamble path: %q", out)
	}
	if _, err := os.Stat(filepath.Join(a.ProjectRoot, "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("dry run created AGENTS.md")
	}
}

// A real (non-dry) index run must still write, otherwise the DryRun
// guard would have broken the primary path.
func TestCmdIndex_RealRunWrites(t *testing.T) {
	a, stdout := newPreambleTestApp(t)
	writePreamble(t, a, testPreamble)

	if err := a.CmdIndex(); err != nil {
		t.Fatalf("CmdIndex returned %v", err)
	}

	if !strings.Contains(readAgentsMD(t, a), "# Engineering Principles") {
		t.Error("real index run did not write the preamble")
	}
	if !strings.Contains(stdout.String(), "Regenerated AGENTS.md") {
		t.Errorf("real run did not report regeneration: %q", stdout.String())
	}
}
