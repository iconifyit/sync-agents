package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// asTool fetches a registered tool by ID or fails the test. Tests
// in this file are about routing, not registry presence, so an
// unregistered tool would be a setup bug.
func asTool(t *testing.T, id string) Tool {
	t.Helper()
	tool, ok := ResolveTool(id)
	if !ok {
		t.Fatalf("tool %q is not registered", id)
	}
	return tool
}

// TestTargetDestination_ClaudeInvocableSkill exercises the most
// common Claude case: an invocable skill lands at
// claude/skills/<name> as a link to the whole skill directory, so
// the skill's supporting files are reachable next to its SKILL.md.
func TestTargetDestination_ClaudeInvocableSkill(t *testing.T) {
	source := filepath.Join("/home/u", ".agents", "skills", "cool")
	d := TargetDestination(asTool(t, "claude"), ArtifactSkill, "cool", Invocable, source, "/home/u")
	if d.Strategy != StrategySymlink {
		t.Errorf("strategy = %v, want StrategySymlink", d.Strategy)
	}
	want := filepath.Join("/home/u", ".claude", "skills", "cool")
	if d.Path != want {
		t.Errorf("path = %q, want %q", d.Path, want)
	}
	if d.Target != source {
		t.Errorf("target = %q, want the skill directory %q", d.Target, source)
	}
}

// TestTargetDestination_NonClaudeSkillTargetsSkillFile pins that only
// Claude links the skill directory; the other symlink tools still
// link the single SKILL.md file their layouts expect.
func TestTargetDestination_NonClaudeSkillTargetsSkillFile(t *testing.T) {
	source := filepath.Join("/home/u", ".agents", "skills", "cool")
	d := TargetDestination(asTool(t, "cursor"), ArtifactSkill, "cool", Invocable, source, "/home/u")
	if want := filepath.Join(source, "SKILL.md"); d.Target != want {
		t.Errorf("cursor target = %q, want %q", d.Target, want)
	}
}

// TestTargetDestination_ClaudeInvocableRule covers the "rule with
// invocable: true frontmatter" override. It must NOT land in rules/;
// instead it goes to commands/ as a slash command.
func TestTargetDestination_ClaudeInvocableRule(t *testing.T) {
	d := TargetDestination(asTool(t, "claude"), ArtifactRule, "onboarding", Invocable, "", "/home/u")
	want := filepath.Join("/home/u", ".claude", "commands", "onboarding.md")
	if d.Path != want {
		t.Errorf("path = %q, want %q (invocable rule must land in commands/, not rules/)", d.Path, want)
	}
}

// TestTargetDestination_ClaudePassiveRule is the default case for
// rules: passive semantic → claude/rules/<name>.md.
func TestTargetDestination_ClaudePassiveRule(t *testing.T) {
	d := TargetDestination(asTool(t, "claude"), ArtifactRule, "security", Passive, "", "/home/u")
	want := filepath.Join("/home/u", ".claude", "rules", "security.md")
	if d.Path != want {
		t.Errorf("path = %q, want %q", d.Path, want)
	}
}

// TestTargetDestination_ClaudePassiveSkill is the awkward edge case:
// a skill (directory) explicitly marked passive has no clean Claude
// destination. Today we skip with a warning rather than guessing.
func TestTargetDestination_ClaudePassiveSkill(t *testing.T) {
	d := TargetDestination(asTool(t, "claude"), ArtifactSkill, "x", Passive, "", "/home/u")
	if d.Strategy != StrategySkip {
		t.Errorf("strategy = %v, want StrategySkip (no clean dest for passive skill)", d.Strategy)
	}
	if d.SkipReason == "" {
		t.Error("SkipReason should explain why")
	}
}

// TestTargetDestination_CodeiumInvocableSingleFile covers the
// "Claude skill → Windsurf workflow" routing for a single-file skill
// (only SKILL.md inside the dir; no supporting files).
func TestTargetDestination_CodeiumInvocableSingleFile(t *testing.T) {
	// Set up a single-file skill so SkillIsMultiFile returns false.
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "cool")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# cool\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	d := TargetDestination(asTool(t, "codeium"), ArtifactSkill, "cool", Invocable, skillDir, "/home/u")
	if d.Strategy != StrategySymlink {
		t.Errorf("strategy = %v, want StrategySymlink", d.Strategy)
	}
	want := filepath.Join("/home/u", ".codeium", "windsurf", "global_workflows", "cool.md")
	if d.Path != want {
		t.Errorf("path = %q, want %q", d.Path, want)
	}
}

// TestTargetDestination_CodeiumInvocableMultiFile covers the
// multi-file skill skip case. Add a helper.txt next to SKILL.md and
// the destination must be StrategySkip with a reason mentioning the
// skill name.
func TestTargetDestination_CodeiumInvocableMultiFile(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "big")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	for _, f := range []string{"SKILL.md", "helper.txt"} {
		if err := os.WriteFile(filepath.Join(skillDir, f), []byte("x"), 0o644); err != nil {
			t.Fatalf("setup %s: %v", f, err)
		}
	}

	d := TargetDestination(asTool(t, "codeium"), ArtifactSkill, "big", Invocable, skillDir, "/home/u")
	if d.Strategy != StrategySkip {
		t.Errorf("strategy = %v, want StrategySkip", d.Strategy)
	}
	if d.SkipReason == "" || !contains(d.SkipReason, "big") {
		t.Errorf("SkipReason %q should mention skill name %q", d.SkipReason, "big")
	}
}

// TestTargetDestination_CodeiumPassiveConcat checks the rule concat
// destination — Windsurf's memories/global_rules.md.
func TestTargetDestination_CodeiumPassiveConcat(t *testing.T) {
	d := TargetDestination(asTool(t, "codeium"), ArtifactRule, "security", Passive, "", "/home/u")
	if d.Strategy != StrategyConcat {
		t.Errorf("strategy = %v, want StrategyConcat", d.Strategy)
	}
	want := filepath.Join("/home/u", ".codeium", "windsurf", "memories", "global_rules.md")
	if d.Path != want {
		t.Errorf("path = %q, want %q", d.Path, want)
	}
}

// TestTargetDestination_CursorDegenerate verifies that both
// semantics resolve to the same Cursor path.
func TestTargetDestination_CursorDegenerate(t *testing.T) {
	cursor := asTool(t, "cursor")
	dPassive := TargetDestination(cursor, ArtifactRule, "x", Passive, "", "/home/u")
	dInvocable := TargetDestination(cursor, ArtifactSkill, "x", Invocable, "", "/home/u")

	want := filepath.Join("/home/u", ".cursor", "rules", "x.md")
	if dPassive.Path != want || dInvocable.Path != want {
		t.Errorf("Cursor should route both semantics to %q; got passive=%q invocable=%q",
			want, dPassive.Path, dInvocable.Path)
	}
	if dPassive.Strategy != StrategySymlink || dInvocable.Strategy != StrategySymlink {
		t.Error("both Cursor destinations should be StrategySymlink")
	}
}

// TestTargetDestination_CopilotAndCodexConcat verifies both
// instructions.md routes work and that semantic is ignored.
func TestTargetDestination_CopilotAndCodexConcat(t *testing.T) {
	cases := []struct {
		toolID string
		want   string
	}{
		{"copilot", filepath.Join("/home/u", ".github", "copilot", "instructions.md")},
		{"codex", filepath.Join("/home/u", ".codex", "instructions.md")},
	}
	for _, c := range cases {
		t.Run(c.toolID, func(t *testing.T) {
			tool := asTool(t, c.toolID)
			for _, sem := range []Semantic{Passive, Invocable} {
				d := TargetDestination(tool, ArtifactRule, "x", sem, "", "/home/u")
				if d.Strategy != StrategyConcat {
					t.Errorf("%s/%s strategy = %v, want StrategyConcat", c.toolID, sem, d.Strategy)
				}
				if d.Path != c.want {
					t.Errorf("%s/%s path = %q, want %q", c.toolID, sem, d.Path, c.want)
				}
			}
		})
	}
}

// TestSkillIsMultiFile_SingleFile creates a SKILL.md-only dir and
// asserts the function returns false.
func TestSkillIsMultiFile_SingleFile(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if SkillIsMultiFile(tmp) {
		t.Error("single-file skill reported as multi-file")
	}
}

// TestSkillIsMultiFile_MultiFile adds a sibling file and asserts
// the function flips to true.
func TestSkillIsMultiFile_MultiFile(t *testing.T) {
	tmp := t.TempDir()
	for _, f := range []string{"SKILL.md", "helper.txt"} {
		if err := os.WriteFile(filepath.Join(tmp, f), []byte("x"), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if !SkillIsMultiFile(tmp) {
		t.Error("multi-file skill reported as single-file")
	}
}

// TestSkillIsMultiFile_NonexistentTreatedAsSingle documents the
// not-found behavior. The function returns false so the symlink
// layer can produce a clearer "source missing" error than a fake
// warning would.
func TestSkillIsMultiFile_NonexistentTreatedAsSingle(t *testing.T) {
	if SkillIsMultiFile("/does/not/exist") {
		t.Error("nonexistent path should not be reported as multi-file")
	}
}

// TestTargetDestination_AgentClaude routes subagent definitions to
// Claude's agents/ surface regardless of semantic (SPEC-004 Part B).
func TestTargetDestination_AgentClaude(t *testing.T) {
	for _, sem := range []Semantic{Invocable, Passive} {
		d := TargetDestination(asTool(t, "claude"), ArtifactAgent, "reviewer", sem, "", "/home/u")
		if d.Strategy != StrategySymlink {
			t.Errorf("sem=%s strategy = %v, want StrategySymlink", sem, d.Strategy)
		}
		want := filepath.Join("/home/u", ".claude", "agents", "reviewer.md")
		if d.Path != want {
			t.Errorf("sem=%s path = %q, want %q", sem, d.Path, want)
		}
	}
}

// TestTargetDestination_AgentOtherToolsSkip: no other registered tool
// has a subagent surface — every non-Claude tool must skip, never
// concat or mislabel the agent as a workflow/command.
func TestTargetDestination_AgentOtherToolsSkip(t *testing.T) {
	for _, id := range []string{"codeium", "cursor", "copilot", "codex"} {
		d := TargetDestination(asTool(t, id), ArtifactAgent, "reviewer", Invocable, "", "/home/u")
		if d.Strategy != StrategySkip {
			t.Errorf("[%s] strategy = %v, want StrategySkip", id, d.Strategy)
		}
		if d.SkipReason == "" {
			t.Errorf("[%s] SkipReason must be set", id)
		}
	}
}

// TestTargetDestination_ReferenceDocsClaude routes plans/specs to
// .claude/plans|specs regardless of semantic (SPEC-004 Part D).
func TestTargetDestination_ReferenceDocsClaude(t *testing.T) {
	cases := []struct {
		typ ArtifactType
		dir string
	}{
		{ArtifactPlan, "plans"},
		{ArtifactSpec, "specs"},
	}
	for _, c := range cases {
		d := TargetDestination(asTool(t, "claude"), c.typ, "roadmap", Reference, "", "/home/u")
		if d.Strategy != StrategySymlink {
			t.Errorf("%s strategy = %v, want StrategySymlink", c.typ, d.Strategy)
		}
		want := filepath.Join("/home/u", ".claude", c.dir, "roadmap.md")
		if d.Path != want {
			t.Errorf("%s path = %q, want %q", c.typ, d.Path, want)
		}
	}
}

// TestTargetDestination_ReferenceDocsOtherToolsSkip: plans/specs are
// never concatenated into always-on instruction files.
func TestTargetDestination_ReferenceDocsOtherToolsSkip(t *testing.T) {
	for _, id := range []string{"codeium", "cursor", "copilot", "codex"} {
		for _, typ := range []ArtifactType{ArtifactPlan, ArtifactSpec} {
			d := TargetDestination(asTool(t, id), typ, "roadmap", Reference, "", "/home/u")
			if d.Strategy != StrategySkip {
				t.Errorf("[%s] %s strategy = %v, want StrategySkip", id, typ, d.Strategy)
			}
		}
	}
}
