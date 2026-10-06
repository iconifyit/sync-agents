package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newLocalIndexTestApp returns an App pointed at a fresh t.TempDir
// as the project root, with a minimal .agents/rules/ directory and a
// passive rule ready to index. Stdout/Stderr are captured buffers.
func newLocalIndexTestApp(t *testing.T) (*App, string, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	agentsRules := filepath.Join(root, ".agents", "rules")
	if err := os.MkdirAll(agentsRules, 0o755); err != nil {
		t.Fatalf("setup rules dir: %v", err)
	}
	stdout := &bytes.Buffer{}
	a := &App{
		ProjectRoot:   root,
		GlobalRoot:    filepath.Join(root, ".agents"),
		ActiveTargets: []string{"claude"},
		Stdout:        stdout,
		Stderr:        &bytes.Buffer{},
	}
	return a, root, stdout
}

// TestCmdIndex_WritesClaudeImportsBlock verifies that local
// `sync-agents index` produces an AGENTS.md with a managed
// @-import block listing each passive rule using project-relative
// paths — the bridge that makes Claude actually load the rule
// content (see issue #46).

// TestCmdIndex_EmptyAgentsTreeOmitsBlock verifies that a fresh
// project with no rules/skills/workflows doesn't write an empty @-
// import block (there's nothing to import).
func TestCmdIndex_EmptyAgentsTreeOmitsBlock(t *testing.T) {
	a, root, _ := newLocalIndexTestApp(t)

	if err := a.CmdIndex(); err != nil {
		t.Fatalf("CmdIndex: %v", err)
	}

	agentsMD := filepath.Join(root, "AGENTS.md")
	data, err := os.ReadFile(agentsMD)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	s := string(data)

	if HasManagedImportBlock(s) {
		t.Errorf("empty @-import block written when no passive rules exist:\n%s", s)
	}
}

// TestCmdIndex_StripsStaleBlock verifies that re-indexing after the
// user deletes all rules removes the managed block entirely rather
// than leaving a dead @-import list behind.
func TestCmdIndex_StripsStaleBlock(t *testing.T) {
	a, root, _ := newLocalIndexTestApp(t)

	// Seed + index once.
	if err := os.WriteFile(filepath.Join(root, ".agents", "rules", "security.md"), []byte("body\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := a.CmdIndex(); err != nil {
		t.Fatalf("first CmdIndex: %v", err)
	}

	// Now delete the rule and re-index.
	if err := os.Remove(filepath.Join(root, ".agents", "rules", "security.md")); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if err := a.CmdIndex(); err != nil {
		t.Fatalf("second CmdIndex: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	s := string(data)
	if HasManagedImportBlock(s) {
		t.Errorf("stale managed block not stripped after rule removal:\n%s", s)
	}
}

// TestArtifactDescription covers description extraction edge cases.
func TestArtifactDescription(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "simple description",
			content: `---
description: Enforces security policies across the repository
---
# Rule
Body.`,
			want: "Enforces security policies across the repository",
		},
		{
			name:    "no frontmatter",
			content: `# Just a markdown file\nNo frontmatter here.`,
			want:    "",
		},
		{
			name: "TODO stub suppressed",
			content: `---
description: TODO — describe this later
---
Body.`,
			want: "",
		},
		{
			name: "multi-line scalar skipped",
			content: `---
description: |
  This is a
  multi-line description
---
Body.`,
			want: "",
		},
		{
			name: "long description truncated",
			content: fmt.Sprintf(`---
description: %s
---
Body.`, strings.Repeat("word ", 50)),
			want: func() string {
				s := strings.TrimSpace(strings.Repeat("word ", 50))
				if len(s) > 140 {
					// artifactDescription joins fields (strings.Fields then Join),
					// then truncates at maxIndexDescription=140 via truncateAtWord.
					// 50 repeats of "word " = 250 chars. Truncate to 140 chars
					// at word boundary, append ellipsis.
					s = truncateAtWord(s, 140) + "…"
				}
				return s
			}(),
		},
		{
			name:    "missing file",
			content: "",
			want:    "",
		},
		{
			name: "empty description",
			content: `---
description:
---
Body.`,
			want: "",
		},
		{
			name: "unterminated frontmatter",
			content: `---
description: something bad
Body without closing.`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			if tt.content != "" {
				f, err := os.CreateTemp(t.TempDir(), "test-*.md")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString(tt.content); err != nil {
					t.Fatal(err)
				}
				f.Close()
				path = f.Name()
			} else {
				path = filepath.Join(t.TempDir(), "nonexistent.md")
			}
			got := artifactDescription(path)
			if got != tt.want {
				t.Errorf("artifactDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIndexEntry covers the rendering of AGENTS.md index lines.
func TestIndexEntry(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name     string
		md       string
		linkPath string
	}{
		{
			name:     "with description",
			md:       "---\ndescription: Does something useful\n---\nBody.",
			linkPath: ".agents/rules/useful.md",
		},
		{
			name:     "without description",
			md:       "# No frontmatter",
			linkPath: ".agents/rules/boring.md",
		},
		{
			name:     "stub description suppressed",
			md:       "---\ndescription: TODO fill me in\n---\nBody.",
			linkPath: ".agents/rules/wip.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := os.CreateTemp(dir, "test-*.md")
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString(tt.md)
			f.Close()

			got := indexEntry(tt.name, tt.linkPath, f.Name())
			if !strings.Contains(got, tt.linkPath) {
				t.Errorf("indexEntry missing link %q in:\n%s", tt.linkPath, got)
			}
			if !strings.Contains(got, tt.name) {
				t.Errorf("indexEntry missing name %q in:\n%s", tt.name, got)
			}
			if strings.Contains(got, "TODO") {
				t.Errorf("TODO should be suppressed in:\n%s", got)
			}
		})
	}
}

func TestIsBucketActive(t *testing.T) {
	app := &App{ActiveTargets: []string{"claude", "cursor"}}
	if !app.isBucketActive("claude") {
		t.Error("claude should be active")
	}
	if !app.isBucketActive("cursor") {
		t.Error("cursor should be active")
	}
	if app.isBucketActive("copilot") {
		t.Error("copilot should NOT be active")
	}
	if app.isBucketActive("") {
		t.Error("empty string should NOT be active")
	}
}

func TestResolveTargetDir(t *testing.T) {
	root := "/project"
	tests := []struct {
		target string
		want   string
	}{
		{"claude", "/project/.claude"},
		{"cursor", "/project/.cursor"},
		{"windsurf", "/project/.windsurf"},
		{"copilot", "/project/.github/copilot"},
		{"codex", "/project/.codex"},
	}
	for _, tt := range tests {
		got := ResolveTargetDir(tt.target, root)
		if got != tt.want {
			t.Errorf("ResolveTargetDir(%q, %q) = %q, want %q", tt.target, root, got, tt.want)
		}
	}
}

func TestResolveAgentsRel(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"claude", "../.agents"},
		{"windsurf", "../.agents"},
		{"cursor", "../.agents"},
		{"copilot", "../../.agents"},
	}
	for _, tt := range tests {
		got := ResolveAgentsRel(tt.target)
		if got != tt.want {
			t.Errorf("ResolveAgentsRel(%q) = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestListMDFilesRecursive_Nested(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "effort-a"), 0o755)
	os.MkdirAll(filepath.Join(dir, "effort-b"), 0o755)
	os.WriteFile(filepath.Join(dir, "plan.md"), []byte("root"), 0o644)
	os.WriteFile(filepath.Join(dir, "effort-a", "rollout.md"), []byte("nested"), 0o644)
	os.WriteFile(filepath.Join(dir, "effort-b", "testing.md"), []byte("nested2"), 0o644)
	os.WriteFile(filepath.Join(dir, ".hidden.md"), []byte("hidden"), 0o644)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not md"), 0o644)

	names, warns := listMDFilesRecursive(dir)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(names) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(names), names)
	}
	want := map[string]bool{
		"plan":                 true,
		"effort-a/rollout":     true,
		"effort-b/testing":     true,
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected file: %q", n)
		}
	}
}

func TestCreateSymlink_Repair(t *testing.T) {
	dir := t.TempDir()
	var buf strings.Builder
	app := &App{ProjectRoot: dir, Stdout: &buf, Stderr: &buf, Force: true}

	os.MkdirAll(filepath.Join(dir, ".agents", "rules"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agents", "rules", "test.md"), []byte("content"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".claude", "rules"), 0o755)

	target := filepath.Join(dir, ".claude", "rules", "test.md")
	os.Symlink("wrong-target", target)

	app.CreateSymlink(".agents/rules/test.md", target, false)

	link, _ := os.Readlink(target)
	if link != ".agents/rules/test.md" {
		t.Errorf("symlink not repaired: got %q", link)
	}
}

func TestGenerateAgentsMD_AllSections(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".agents", "rules"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".agents", "skills", "my-skill"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".agents", "workflows"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".agents", "agents"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".agents", "hooks"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".agents", "plans", "effort-x"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".agents", "specs"), 0o755)

	os.WriteFile(filepath.Join(dir, ".agents", "rules", "security.md"), []byte("---\ndescription: locks down\n---\nbody"), 0o644)
	os.WriteFile(filepath.Join(dir, ".agents", "skills", "my-skill", "SKILL.md"), []byte("---\nname: my-skill\ndescription: Does cool stuff\n---\nbody"), 0o644)
	os.WriteFile(filepath.Join(dir, ".agents", "workflows", "deploy.md"), []byte("body"), 0o644)
	os.WriteFile(filepath.Join(dir, ".agents", "agents", "reviewer.md"), []byte("body"), 0o644)
	os.WriteFile(filepath.Join(dir, ".agents", "hooks", "lint.json"), []byte(`{"event":"Stop"}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".agents", "plans", "effort-x", "rollout.md"), []byte("body"), 0o644)
	os.WriteFile(filepath.Join(dir, ".agents", "specs", "design.md"), []byte("body"), 0o644)

	var buf bytes.Buffer
	app := &App{
		ProjectRoot:   dir,
		GlobalRoot:    filepath.Join(dir, ".agents"),
		ActiveTargets: []string{"claude"},
		Stdout:        &buf,
		Stderr:        &buf,
	}
	app.generateAgentsMD()

	data, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	content := string(data)
	for _, want := range []string{
		"## Rules", "## Skills", "## Workflows", "## Agents",
		"## Hooks", "## Plans", "## Specs",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("missing section %q in AGENTS.md:\n%s", want, content[:min(len(content), 200)])
		}
	}
	// Description renders in rules.
	if !strings.Contains(content, "locks down") {
		t.Errorf("description not rendered:\n%s", content[:min(len(content), 500)])
	}
	// Hooks list the .json file.
	if !strings.Contains(content, "lint.json") {
		t.Errorf("hook file not indexed:\n%s", content[:min(len(content), 500)])
	}
}

func TestGenerateAgentsMD_EmptyTree(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".agents"), 0o755)
	var buf bytes.Buffer
	app := &App{
		ProjectRoot:   dir,
		GlobalRoot:    filepath.Join(dir, ".agents"),
		ActiveTargets: []string{"claude"},
		Stdout:        &buf,
		Stderr:        &buf,
	}
	app.generateAgentsMD()

	data, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	content := string(data)
	for _, want := range []string{
		"No rules defined yet", "No skills defined yet", "No workflows defined yet",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("missing placeholder %q:\n%s", want, content[:200])
		}
	}
	// New buckets without content should not appear.
	if strings.Contains(content, "## Agents") || strings.Contains(content, "## Plans") {
		t.Errorf("empty optional bucket should not appear in index")
	}
}

func TestCmdIndex_WithStateSnapshots(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".agents", "rules"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agents", "rules", "test.md"), []byte("body"), 0o644)

	// Add a state snapshot.
	os.WriteFile(filepath.Join(dir, ".agents", "STATE_test_2026-07-02.md"), []byte("state content"), 0o644)

	var buf bytes.Buffer
	app := &App{
		ProjectRoot:   dir,
		GlobalRoot:    filepath.Join(dir, ".agents"),
		ActiveTargets: []string{"claude"},
		Stdout:        &buf,
		Stderr:        &buf,
	}

	if err := app.CmdIndex(); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	content := string(data)
	if !strings.Contains(content, "STATE_test") {
		t.Errorf("state snapshot not indexed:\n%s", content[:500])
	}
}

func TestBucketForArtifact_Hooks(t *testing.T) {
	if _, ok := BucketForArtifact(ArtifactHook); !ok {
		t.Error("ArtifactHook should be found")
	}
}

func TestCmdIndex_NoFix(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".agents", "rules"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agents", "rules", "test.md"), []byte("body"), 0o644)

	var buf bytes.Buffer
	app := &App{
		ProjectRoot:   dir,
		GlobalRoot:    filepath.Join(dir, ".agents"),
		ActiveTargets: []string{"claude"},
		Stdout:        &buf,
		Stderr:        &buf,
	}

	// Just verify CmdIndex doesn't fail — no-fix is handled at CLI layer.
	if err := app.CmdIndex(); err != nil {
		t.Fatal(err)
	}
}
