package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedAgentsRepo builds a realistic agents repo: a root holding
// AGENTS.md plus a .agents/ tree, with the global root reaching it
// through a symlink the way ~/.agents does in production.
func seedAgentsRepo(t *testing.T, agentsMD string) (repoRoot, globalRootSymlink string) {
	t.Helper()
	base := t.TempDir()
	repoRoot = filepath.Join(base, "agents-repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".agents", "rules"), 0o755); err != nil {
		t.Fatalf("seeding repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "AGENTS.md"), []byte(agentsMD), 0o644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	// ~/.agents -> <repo>/.agents
	globalRootSymlink = filepath.Join(base, "home", ".agents")
	if err := os.MkdirAll(filepath.Dir(globalRootSymlink), 0o755); err != nil {
		t.Fatalf("seeding home: %v", err)
	}
	if err := os.Symlink(filepath.Join(repoRoot, ".agents"), globalRootSymlink); err != nil {
		t.Fatalf("symlinking global root: %v", err)
	}
	return repoRoot, globalRootSymlink
}

// The global root is conventionally a symlink into the agents repo, and
// AGENTS.md lives beside the REAL .agents directory. Resolving the
// parent lexically would look next to the symlink (i.e. $HOME) and find
// nothing, silently disabling the mirror.
func TestResolveGlobalAgentsMDPath_FollowsSymlinkedGlobalRoot(t *testing.T) {
	repoRoot, globalRoot := seedAgentsRepo(t, "# AGENTS\n")

	got := ResolveGlobalAgentsMDPath(globalRoot)

	want, err := filepath.EvalSymlinks(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("resolving expected path: %v", err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolving got path %q: %v", got, err)
	}
	if gotResolved != want {
		t.Errorf("ResolveGlobalAgentsMDPath() = %q, want %q", gotResolved, want)
	}
}

func TestResolveGlobalAgentsMDPath_EmptyWhenAbsent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, ".agents")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if got := ResolveGlobalAgentsMDPath(root); got != "" {
		t.Errorf("expected empty path when AGENTS.md is absent, got %q", got)
	}
}

// AGENTS.md indexes artifacts relative to the agents repo. Mirrored
// verbatim into ~/.claude/CLAUDE.md those resolve to
// ~/.claude/.agents/... and dangle, so they must be absolutized.
// The rewrite must land on the REAL directory, not the `.agents/`
// symlink overlay. Both resolve to the same file, but readers that
// decline to follow symlinks (VS Code with search.followSymlinks off)
// report the `.agents/` form as missing and offer to create it.
func TestAbsolutizeAgentsMDLinks(t *testing.T) {
	in := "- [adr-required](.agents/rules/adr-required.md)\n- [plan](.agents/skills/plan/SKILL.md)\n"
	out := AbsolutizeAgentsMDLinks(in, "/repo")

	if strings.Contains(out, "](.agents/") {
		t.Errorf("relative link survived absolutization:\n%s", out)
	}
	for _, want := range []string{"](/repo/rules/adr-required.md)", "](/repo/skills/plan/SKILL.md)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/repo/.agents/") {
		t.Errorf("link still routes through the .agents symlink overlay:\n%s", out)
	}
}

// The rewritten path must reach the same file as the .agents/ form.
// This is what makes dropping the segment safe rather than merely
// cosmetic: .agents/<bucket> is a symlink to ../<bucket>.
func TestAbsolutizeAgentsMDLinks_RewrittenPathReachesTheSameFile(t *testing.T) {
	repoRoot, _ := seedAgentsRepo(t, "# AGENTS\n")
	rule := filepath.Join(repoRoot, "rules", "demo.md")
	if err := os.MkdirAll(filepath.Dir(rule), 0o755); err != nil {
		t.Fatalf("seeding rules dir: %v", err)
	}
	if err := os.WriteFile(rule, []byte("# demo\n"), 0o644); err != nil {
		t.Fatalf("seeding rule: %v", err)
	}
	// seedAgentsRepo makes .agents/rules a real directory; production
	// has it as a symlink to ../rules, which is the shape under test.
	overlay := filepath.Join(repoRoot, ".agents", "rules")
	if err := os.RemoveAll(overlay); err != nil {
		t.Fatalf("clearing overlay dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(repoRoot, "rules"), overlay); err != nil {
		t.Fatalf("symlinking .agents/rules: %v", err)
	}

	out := AbsolutizeAgentsMDLinks("[demo](.agents/rules/demo.md)", repoRoot)

	start := strings.Index(out, "](") + 2
	got := out[start : len(out)-1]
	if _, err := os.Lstat(got); err != nil {
		t.Fatalf("rewritten path does not exist without following a symlink: %s (%v)", got, err)
	}
	viaOverlay := filepath.Join(repoRoot, ".agents", "rules", "demo.md")
	a, _ := filepath.EvalSymlinks(got)
	b, _ := filepath.EvalSymlinks(viaOverlay)
	if a != b {
		t.Errorf("rewritten path resolves elsewhere:\n  got:     %s -> %s\n  overlay: %s -> %s", got, a, viaOverlay, b)
	}
}

func TestAbsolutizeAgentsMDLinks_LeavesOtherLinksAlone(t *testing.T) {
	in := "[docs](https://example.com/.agents/x.md) and [rel](./notes.md)\n"
	if got := AbsolutizeAgentsMDLinks(in, "/repo"); got != in {
		t.Errorf("non-repo-relative links were rewritten:\ngot:  %s\nwant: %s", got, in)
	}
}

// The whole point of the feature: preamble content reaches CLAUDE.md.
func TestRegenerateAgentsMDBlock_MirrorsContentAndPreservesOutsideText(t *testing.T) {
	_, globalRoot := seedAgentsRepo(t, "# AGENTS\n\n# Principles\n\nPREAMBLE_CANARY\n\n## Rules\n\n- [r](.agents/rules/r.md)\n")
	claudeMD := filepath.Join(t.TempDir(), ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(claudeMD), 0o755); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := os.WriteFile(claudeMD, []byte("# Hand-written\n\nMUST_SURVIVE\n"), 0o644); err != nil {
		t.Fatalf("seeding CLAUDE.md: %v", err)
	}

	changed, err := RegenerateAgentsMDBlock(claudeMD, ResolveGlobalAgentsMDPath(globalRoot))
	if err != nil {
		t.Fatalf("RegenerateAgentsMDBlock: %v", err)
	}
	if !changed {
		t.Fatal("expected the file to change")
	}

	got := readFileString(t, claudeMD)
	for _, want := range []string{"PREAMBLE_CANARY", "MUST_SURVIVE", AgentsMDBlockStart, AgentsMDBlockEnd} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "](.agents/") {
		t.Errorf("index link was not absolutized:\n%s", got)
	}
}

func TestRegenerateAgentsMDBlock_IsIdempotent(t *testing.T) {
	_, globalRoot := seedAgentsRepo(t, "# AGENTS\n\nPREAMBLE_CANARY\n")
	claudeMD := filepath.Join(t.TempDir(), "CLAUDE.md")
	path := ResolveGlobalAgentsMDPath(globalRoot)

	if _, err := RegenerateAgentsMDBlock(claudeMD, path); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := readFileString(t, claudeMD)

	changed, err := RegenerateAgentsMDBlock(claudeMD, path)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if changed {
		t.Error("second run reported a change; regeneration is not idempotent")
	}
	if second := readFileString(t, claudeMD); second != first {
		t.Error("second run produced different bytes")
	}
	if n := strings.Count(first, "PREAMBLE_CANARY"); n != 1 {
		t.Errorf("content mirrored %d times, want exactly 1", n)
	}
}

// Editing the preamble must propagate — the failure that motivated this.
func TestRegenerateAgentsMDBlock_PicksUpEdits(t *testing.T) {
	repoRoot, globalRoot := seedAgentsRepo(t, "# AGENTS\n\nOLD_PRINCIPLE\n")
	claudeMD := filepath.Join(t.TempDir(), "CLAUDE.md")
	path := ResolveGlobalAgentsMDPath(globalRoot)

	if _, err := RegenerateAgentsMDBlock(claudeMD, path); err != nil {
		t.Fatalf("first run: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repoRoot, "AGENTS.md"), []byte("# AGENTS\n\nNEW_PRINCIPLE\n"), 0o644); err != nil {
		t.Fatalf("editing AGENTS.md: %v", err)
	}
	if _, err := RegenerateAgentsMDBlock(claudeMD, path); err != nil {
		t.Fatalf("second run: %v", err)
	}

	got := readFileString(t, claudeMD)
	if strings.Contains(got, "OLD_PRINCIPLE") {
		t.Error("stale content survived regeneration")
	}
	if !strings.Contains(got, "NEW_PRINCIPLE") {
		t.Error("edited content did not propagate")
	}
}

// The two managed regions must not clobber each other.
func TestAgentsMDBlockAndImportBlockCoexist(t *testing.T) {
	_, globalRoot := seedAgentsRepo(t, "# AGENTS\n\nPREAMBLE_CANARY\n")
	claudeMD := filepath.Join(t.TempDir(), "CLAUDE.md")

	if _, err := RegenerateAgentsMDBlock(claudeMD, ResolveGlobalAgentsMDPath(globalRoot)); err != nil {
		t.Fatalf("agents-md block: %v", err)
	}
	if _, err := RegenerateClaudeImports(claudeMD, []string{"/abs/rules/a.md"}); err != nil {
		t.Fatalf("imports block: %v", err)
	}

	got := readFileString(t, claudeMD)
	for _, want := range []string{
		AgentsMDBlockStart, AgentsMDBlockEnd, "PREAMBLE_CANARY",
		ManagedImportBlockStart, ManagedImportBlockEnd, "@/abs/rules/a.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q after writing both blocks:\n%s", want, got)
		}
	}

	// Regenerating one must not disturb the other.
	if _, err := RegenerateAgentsMDBlock(claudeMD, ResolveGlobalAgentsMDPath(globalRoot)); err != nil {
		t.Fatalf("agents-md rewrite: %v", err)
	}
	after := readFileString(t, claudeMD)
	if !strings.Contains(after, "@/abs/rules/a.md") {
		t.Error("rewriting the AGENTS.md block destroyed the @-import block")
	}
}

func TestRegenerateAgentsMDBlock_NoAgentsMDIsNotAnError(t *testing.T) {
	claudeMD := filepath.Join(t.TempDir(), "CLAUDE.md")
	changed, err := RegenerateAgentsMDBlock(claudeMD, "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if changed {
		t.Error("expected no change when there is no AGENTS.md")
	}
	if _, statErr := os.Stat(claudeMD); statErr == nil {
		t.Error("CLAUDE.md should not have been created")
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
