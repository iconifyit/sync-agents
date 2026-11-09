package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newPromoteTestApp returns an App with two isolated trees, one for
// the project and one for the global root. Both are under a single
// t.TempDir so cleanup is automatic and the test never touches the
// real $HOME.
func newPromoteTestApp(t *testing.T) (*App, string, string) {
	t.Helper()
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	globalRoot := filepath.Join(root, "global", ".agents")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("setup project: %v", err)
	}
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("setup global: %v", err)
	}
	return &App{
		ProjectRoot: projectRoot,
		GlobalRoot:  globalRoot,
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
	}, projectRoot, globalRoot
}

// writeLocalArtifact creates a project-side artifact at the
// canonical .agents/ path and returns the absolute path for
// assertion convenience.
func writeLocalArtifact(t *testing.T, projectRoot string, typ ArtifactType, name, content string) string {
	t.Helper()
	rel := artifactRelPath(typ, name)
	abs := filepath.Join(projectRoot, rel)
	if typ == ArtifactSkill {
		// Skill is a directory; we write SKILL.md inside it.
		if err := os.MkdirAll(abs, 0o755); err != nil {
			t.Fatalf("setup skill dir: %v", err)
		}
		skillMD := filepath.Join(abs, "SKILL.md")
		if err := os.WriteFile(skillMD, []byte(content), 0o644); err != nil {
			t.Fatalf("setup SKILL.md: %v", err)
		}
		return skillMD
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("setup parent dir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("setup artifact: %v", err)
	}
	return abs
}

// TestNormalizeArtifactType covers the singular/plural/case
// normalization documented on NormalizeArtifactType.
func TestNormalizeArtifactType(t *testing.T) {
	cases := []struct {
		in   string
		want ArtifactType
		ok   bool
	}{
		{"rule", ArtifactRule, true},
		{"rules", ArtifactRule, true},
		{"Rule", ArtifactRule, true},
		{"SKILL", ArtifactSkill, true},
		{"workflows", ArtifactWorkflow, true},
		{"command", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, ok := NormalizeArtifactType(c.in)
			if ok != c.ok {
				t.Errorf("NormalizeArtifactType(%q) ok=%v, want %v", c.in, ok, c.ok)
			}
			if got != c.want {
				t.Errorf("NormalizeArtifactType(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestDetectArtifact_Skill covers both the "dir form" and the
// "SKILL.md form" since the path-form invocation accepts either.
func TestDetectArtifact_Skill(t *testing.T) {
	cases := []string{
		".agents/skills/cool-skill",
		".agents/skills/cool-skill/SKILL.md",
		"./.agents/skills/cool-skill",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			typ, name, err := DetectArtifact(in)
			if err != nil {
				t.Fatalf("DetectArtifact(%q) returned err: %v", in, err)
			}
			if typ != ArtifactSkill || name != "cool-skill" {
				t.Errorf("DetectArtifact(%q) = (%q, %q), want (skill, cool-skill)", in, typ, name)
			}
		})
	}
}

// TestDetectArtifact_RuleAndWorkflow exercises the file-form paths.
func TestDetectArtifact_RuleAndWorkflow(t *testing.T) {
	cases := []struct {
		in       string
		wantType ArtifactType
		wantName string
	}{
		{".agents/rules/security.md", ArtifactRule, "security"},
		{".agents/workflows/release.md", ArtifactWorkflow, "release"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			typ, name, err := DetectArtifact(c.in)
			if err != nil {
				t.Fatalf("DetectArtifact(%q): %v", c.in, err)
			}
			if typ != c.wantType || name != c.wantName {
				t.Errorf("DetectArtifact(%q) = (%q, %q), want (%q, %q)", c.in, typ, name, c.wantType, c.wantName)
			}
		})
	}
}

// TestDetectArtifact_Unrecognised covers the negative branch: paths
// outside the .agents/{rules,skills,workflows} buckets must error so
// the CLI can suggest the explicit type+name form.
func TestDetectArtifact_Unrecognised(t *testing.T) {
	for _, in := range []string{
		"notes/random.md",
		".agents/",       // bare bucket root
		".agents/rules/", // rule needs .md
		".agents/skills/",
		"foo/bar/baz",
	} {
		t.Run(in, func(t *testing.T) {
			if _, _, err := DetectArtifact(in); err == nil {
				t.Errorf("DetectArtifact(%q) returned no error; want error", in)
			}
		})
	}
}

// TestCmdPromote_Rule covers the happy path for a single-file rule.
// Source content must equal destination content after promote.
func TestCmdPromote_Rule(t *testing.T) {
	a, _, globalRoot := newPromoteTestApp(t)
	writeLocalArtifact(t, a.ProjectRoot, ArtifactRule, "security", "rule body\n")

	if err := a.CmdPromote(ArtifactRule, "security", PromoteOpts{}); err != nil {
		t.Fatalf("CmdPromote: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(globalRoot, "rules", "security.md"))
	if err != nil {
		t.Fatalf("expected rule to be at global root: %v", err)
	}
	if string(got) != "rule body\n" {
		t.Errorf("global content = %q, want %q", got, "rule body\n")
	}
}

// TestCmdPromote_Skill exercises the directory deep-copy. We populate
// the skill with both SKILL.md and a supporting file so the test
// catches a regression where only top-level files get copied.
func TestCmdPromote_Skill(t *testing.T) {
	a, _, globalRoot := newPromoteTestApp(t)
	writeLocalArtifact(t, a.ProjectRoot, ArtifactSkill, "cool", "# cool skill\n")
	supporting := filepath.Join(a.ProjectRoot, ".agents", "skills", "cool", "helper.txt")
	if err := os.WriteFile(supporting, []byte("aux content"), 0o644); err != nil {
		t.Fatalf("setup supporting: %v", err)
	}

	if err := a.CmdPromote(ArtifactSkill, "cool", PromoteOpts{}); err != nil {
		t.Fatalf("CmdPromote: %v", err)
	}

	skillMD, err := os.ReadFile(filepath.Join(globalRoot, "skills", "cool", "SKILL.md"))
	if err != nil {
		t.Fatalf("global SKILL.md missing: %v", err)
	}
	if string(skillMD) != "# cool skill\n" {
		t.Errorf("SKILL.md content = %q, want %q", skillMD, "# cool skill\n")
	}
	helper, err := os.ReadFile(filepath.Join(globalRoot, "skills", "cool", "helper.txt"))
	if err != nil {
		t.Fatalf("supporting file not copied: %v", err)
	}
	if string(helper) != "aux content" {
		t.Errorf("helper.txt content = %q, want %q", helper, "aux content")
	}
}

// TestCmdPromote_Workflow is the third bucket type. Combined with
// the rule and skill tests, the three buckets are covered.
func TestCmdPromote_Workflow(t *testing.T) {
	a, _, globalRoot := newPromoteTestApp(t)
	writeLocalArtifact(t, a.ProjectRoot, ArtifactWorkflow, "release", "workflow body\n")

	if err := a.CmdPromote(ArtifactWorkflow, "release", PromoteOpts{}); err != nil {
		t.Fatalf("CmdPromote: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(globalRoot, "workflows", "release.md"))
	if err != nil {
		t.Fatalf("global workflow missing: %v", err)
	}
	if string(got) != "workflow body\n" {
		t.Errorf("workflow content = %q, want %q", got, "workflow body\n")
	}
}

// TestCmdPromote_ConflictNoForce documents the safety contract:
// destination exists, --force not passed, command fails, content
// unchanged.
func TestCmdPromote_ConflictNoForce(t *testing.T) {
	a, _, globalRoot := newPromoteTestApp(t)
	writeLocalArtifact(t, a.ProjectRoot, ArtifactRule, "x", "new\n")

	dst := filepath.Join(globalRoot, "rules", "x.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := a.CmdPromote(ArtifactRule, "x", PromoteOpts{}); err == nil {
		t.Fatal("expected promote to fail with no --force; got nil")
	}

	got, _ := os.ReadFile(dst)
	if string(got) != "old\n" {
		t.Errorf("destination was modified despite no --force; got %q", got)
	}
}

// TestCmdPromote_ConflictForce flips the previous case: --force=true
// overwrites the destination.
func TestCmdPromote_ConflictForce(t *testing.T) {
	a, _, globalRoot := newPromoteTestApp(t)
	writeLocalArtifact(t, a.ProjectRoot, ArtifactRule, "x", "new\n")

	dst := filepath.Join(globalRoot, "rules", "x.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := a.CmdPromote(ArtifactRule, "x", PromoteOpts{Force: true}); err != nil {
		t.Fatalf("CmdPromote --force: %v", err)
	}

	got, _ := os.ReadFile(dst)
	if string(got) != "new\n" {
		t.Errorf("destination not overwritten with --force; got %q", got)
	}
}

// TestCmdPromote_DryRun ensures no filesystem changes occur and the
// plan message is printed.
func TestCmdPromote_DryRun(t *testing.T) {
	a, _, globalRoot := newPromoteTestApp(t)
	writeLocalArtifact(t, a.ProjectRoot, ArtifactRule, "x", "body\n")

	if err := a.CmdPromote(ArtifactRule, "x", PromoteOpts{DryRun: true}); err != nil {
		t.Fatalf("CmdPromote dry-run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(globalRoot, "rules", "x.md")); err == nil {
		t.Error("dry-run created the destination; should not have")
	}
	stdoutBuf := a.Stdout.(*bytes.Buffer)
	if !strings.Contains(stdoutBuf.String(), "[dry-run]") {
		t.Errorf("expected '[dry-run]' in stdout; got:\n%s", stdoutBuf.String())
	}
}

// TestCmdPromote_MissingSource verifies the error path. The error
// message must include the typ+name pair so the user knows what was
// looked for.
func TestCmdPromote_MissingSource(t *testing.T) {
	a, _, _ := newPromoteTestApp(t)
	if err := a.CmdPromote(ArtifactRule, "does-not-exist", PromoteOpts{}); err == nil {
		t.Fatal("expected error when source missing; got nil")
	}
	stderr := a.Stderr.(*bytes.Buffer)
	if !strings.Contains(stderr.String(), "does-not-exist") {
		t.Errorf("expected error message to name the artifact; got:\n%s", stderr.String())
	}
}

// TestCmdPromote_AutoCreatesParents covers the "promote into a fresh
// global tree" case. The MkdirAll inside CmdPromote should not
// require a prior `global init`.
func TestCmdPromote_AutoCreatesParents(t *testing.T) {
	a, _, globalRoot := newPromoteTestApp(t)
	// Remove the seeded global root so we exercise the no-skeleton case.
	if err := os.RemoveAll(globalRoot); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writeLocalArtifact(t, a.ProjectRoot, ArtifactRule, "x", "body\n")

	if err := a.CmdPromote(ArtifactRule, "x", PromoteOpts{}); err != nil {
		t.Fatalf("CmdPromote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(globalRoot, "rules", "x.md")); err != nil {
		t.Errorf("expected file under auto-created parent: %v", err)
	}
}
