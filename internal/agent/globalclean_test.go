package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCleanTestApp mirrors the other Test*App helpers: temp roots,
// captured output buffers, isolated from $HOME.
func newCleanTestApp(t *testing.T) (*App, string, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	globalRoot := filepath.Join(root, ".agents")
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	stdout := &bytes.Buffer{}
	return &App{
		ProjectRoot: filepath.Join(root, "project"),
		GlobalRoot:  globalRoot,
		Stdout:      stdout,
		Stderr:      &bytes.Buffer{},
	}, root, stdout
}

// TestSymlinkPointsInto covers the safety guard that decides whether
// a symlink belongs to sync-agents. The agentsRoot prefix must match
// exactly; sibling paths (~/.agents-other/) must not match.
func TestSymlinkPointsInto(t *testing.T) {
	tmp := t.TempDir()
	agentsRoot := filepath.Join(tmp, ".agents")
	otherRoot := filepath.Join(tmp, ".agents-other")
	for _, d := range []string{agentsRoot, otherRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	insideTarget := filepath.Join(agentsRoot, "rules", "x.md")
	if err := os.MkdirAll(filepath.Dir(insideTarget), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(insideTarget, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	insideLink := filepath.Join(tmp, "inside-link")
	if err := os.Symlink(insideTarget, insideLink); err != nil {
		t.Fatalf("setup: %v", err)
	}

	outsideTarget := filepath.Join(otherRoot, "x.md")
	if err := os.WriteFile(outsideTarget, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	outsideLink := filepath.Join(tmp, "outside-link")
	if err := os.Symlink(outsideTarget, outsideLink); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if !symlinkPointsInto(insideLink, agentsRoot) {
		t.Errorf("symlink into agentsRoot should match")
	}
	if symlinkPointsInto(outsideLink, agentsRoot) {
		t.Errorf("symlink into a sibling root must NOT match (prefix-check safety)")
	}
}

// TestFileCarriesBanner covers the regular-file safety gate: only
// files that begin with the canonical banner are sync-agents-owned.
func TestFileCarriesBanner(t *testing.T) {
	tmp := t.TempDir()

	withBanner := filepath.Join(tmp, "with.md")
	if err := os.WriteFile(withBanner, []byte(ConcatBanner+"## x\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !fileCarriesBanner(withBanner) {
		t.Error("file with banner should be detected")
	}

	withoutBanner := filepath.Join(tmp, "without.md")
	if err := os.WriteFile(withoutBanner, []byte("just user content\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if fileCarriesBanner(withoutBanner) {
		t.Error("user-content file must not be detected as banner-bearing")
	}

	// Banner not at the very top must not match.
	bannerLater := filepath.Join(tmp, "later.md")
	if err := os.WriteFile(bannerLater, []byte("user prelude\n"+ConcatBanner), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if fileCarriesBanner(bannerLater) {
		t.Error("banner not at file start must not match — that's a user-edited file")
	}
}

// TestCmdGlobalClean_RemovesSyncedSymlinks is the happy path: after
// `global sync`, calling `global clean` removes every per-artifact
// symlink and concat file. The canonical ~/.agents/ tree stays
// intact.
func TestCmdGlobalClean_RemovesSyncedSymlinks(t *testing.T) {
	a, root, _ := newCleanTestApp(t)
	seedRule(t, a.ResolveGlobalRoot(), "x", "body\n")

	if err := a.CmdGlobalSync(GlobalSyncOpts{}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Sanity check: symlinks exist before clean.
	claudeLink := filepath.Join(root, ".claude", "rules", "x.md")
	if _, err := os.Lstat(claudeLink); err != nil {
		t.Fatalf("setup: expected Claude symlink before clean: %v", err)
	}

	if err := a.CmdGlobalClean(GlobalCleanOpts{}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	// Symlinks gone.
	if _, err := os.Lstat(claudeLink); err == nil {
		t.Errorf("expected %s to be removed", claudeLink)
	}
	// Concat files gone.
	memoryFile := filepath.Join(root, ".codeium", "windsurf", "memories", "global_rules.md")
	if _, err := os.Stat(memoryFile); err == nil {
		t.Errorf("expected concat %s to be removed", memoryFile)
	}
	// Canonical artifact untouched.
	src := filepath.Join(a.ResolveGlobalRoot(), "rules", "x.md")
	if _, err := os.Stat(src); err != nil {
		t.Errorf("canonical artifact removed by clean: %v", err)
	}
}

// TestCmdGlobalClean_LeavesUserOwnedSymlinks creates a symlink at a
// per-tool path that points OUTSIDE the global root — simulating a
// user's hand-curated link — and verifies clean does not touch it.
func TestCmdGlobalClean_LeavesUserOwnedSymlinks(t *testing.T) {
	a, root, _ := newCleanTestApp(t)

	// Manually create a user-owned link pointing at a file outside
	// the global root.
	userTarget := filepath.Join(root, "elsewhere.md")
	if err := os.WriteFile(userTarget, []byte("user content"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	userLink := filepath.Join(root, ".claude", "rules", "mine.md")
	if err := os.MkdirAll(filepath.Dir(userLink), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.Symlink(userTarget, userLink); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := a.CmdGlobalClean(GlobalCleanOpts{}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	// User-owned link survives.
	if _, err := os.Lstat(userLink); err != nil {
		t.Errorf("user-owned symlink was removed: %v", err)
	}
}

// TestCmdGlobalClean_LeavesUserOwnedFiles writes a plain file at a
// concat destination (e.g. user manually wrote an instructions.md)
// and verifies clean preserves it with a warning.
func TestCmdGlobalClean_LeavesUserOwnedFiles(t *testing.T) {
	a, root, stdout := newCleanTestApp(t)

	userFile := filepath.Join(root, ".github", "copilot", "instructions.md")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(userFile, []byte("user-written content, no banner\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := a.CmdGlobalClean(GlobalCleanOpts{}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	if _, err := os.Stat(userFile); err != nil {
		t.Errorf("user-owned file removed by clean: %v", err)
	}
	if !strings.Contains(stdout.String(), "skip non-sync-agents") {
		t.Errorf("expected skip-warning in output:\n%s", stdout.String())
	}
}

// TestCmdGlobalClean_DryRun verifies that --dry-run prevents any
// filesystem writes while still printing the plan.
func TestCmdGlobalClean_DryRun(t *testing.T) {
	a, root, stdout := newCleanTestApp(t)
	seedRule(t, a.ResolveGlobalRoot(), "x", "body\n")
	if err := a.CmdGlobalSync(GlobalSyncOpts{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	a.DryRun = true
	stdout.Reset()

	if err := a.CmdGlobalClean(GlobalCleanOpts{}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	// Symlinks still present.
	claudeLink := filepath.Join(root, ".claude", "rules", "x.md")
	if _, err := os.Lstat(claudeLink); err != nil {
		t.Errorf("dry-run clean removed %s", claudeLink)
	}
	// Plan was printed.
	if !strings.Contains(stdout.String(), "[dry-run]") {
		t.Errorf("expected [dry-run] markers:\n%s", stdout.String())
	}
}

// TestCmdGlobalClean_PrunesEmptyParents verifies that after a clean
// the per-tool root directories (which only existed because of
// sync-agents-owned content) are rmdir'd. User-owned siblings should
// keep their parents alive.
func TestCmdGlobalClean_PrunesEmptyParents(t *testing.T) {
	a, root, _ := newCleanTestApp(t)
	seedRule(t, a.ResolveGlobalRoot(), "x", "body\n")
	if err := a.CmdGlobalSync(GlobalSyncOpts{}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if err := a.CmdGlobalClean(GlobalCleanOpts{}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	// .claude root should be gone (it had only sync-agents content).
	if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		t.Errorf(".claude dir not pruned after clean")
	}
}

// TestCmdGlobalClean_RoundTripWithSync exercises the full lifecycle:
// sync → status (synced) → clean → status (missing). Confirms the
// commands compose without artifacts of one leaking into the next.
func TestCmdGlobalClean_RoundTripWithSync(t *testing.T) {
	a, _, _ := newCleanTestApp(t)
	seedRule(t, a.ResolveGlobalRoot(), "x", "body\n")

	if err := a.CmdGlobalSync(GlobalSyncOpts{}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	stdoutAfterSync := &bytes.Buffer{}
	a.Stdout = stdoutAfterSync
	if err := a.CmdGlobalStatus(GlobalStatusOpts{Targets: []string{"claude"}}); err != nil {
		t.Fatalf("status post-sync: %v", err)
	}
	if !strings.Contains(stdoutAfterSync.String(), "[synced]") {
		t.Errorf("expected [synced] before clean:\n%s", stdoutAfterSync.String())
	}

	if err := a.CmdGlobalClean(GlobalCleanOpts{}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	stdoutAfterClean := &bytes.Buffer{}
	a.Stdout = stdoutAfterClean
	if err := a.CmdGlobalStatus(GlobalStatusOpts{Targets: []string{"claude"}}); err != nil {
		t.Fatalf("status post-clean: %v", err)
	}
	if !strings.Contains(stdoutAfterClean.String(), "[missing]") {
		t.Errorf("expected [missing] after clean:\n%s", stdoutAfterClean.String())
	}
}

// TestCmdGlobalClean_NoGlobalRootErrors verifies the missing-root
// error path.
func TestCmdGlobalClean_NoGlobalRootErrors(t *testing.T) {
	tmp := t.TempDir()
	a := &App{
		ProjectRoot: tmp,
		GlobalRoot:  filepath.Join(tmp, "does-not-exist"),
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
	}
	if err := a.CmdGlobalClean(GlobalCleanOpts{}); err == nil {
		t.Fatal("expected error for missing global root; got nil")
	}
}

// TestCmdGlobalClean_RemovesClaudeSkillLinkOnly: clean removes the
// Claude skill directory link without following it, so the skill's
// source files under ~/.agents are untouched.
func TestCmdGlobalClean_RemovesClaudeSkillLinkOnly(t *testing.T) {
	a, root, _ := newCleanTestApp(t)
	seedSkill(t, a.ResolveGlobalRoot(), "deploy-script", "# deploy-script\n", map[string]string{"deploy-lib.sh": "main\n"})
	if err := a.CmdGlobalSync(GlobalSyncOpts{Targets: []string{"claude"}}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if err := a.CmdGlobalClean(GlobalCleanOpts{Targets: []string{"claude"}}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(root, ".claude", "skills", "deploy-script")); !os.IsNotExist(err) {
		t.Errorf("skill link still present after clean: %v", err)
	}
	for _, f := range []string{"SKILL.md", "deploy-lib.sh"} {
		if _, err := os.Stat(filepath.Join(a.ResolveGlobalRoot(), "skills", "deploy-script", f)); err != nil {
			t.Errorf("clean removed skill source %s: %v", f, err)
		}
	}
}

// TestCmdGlobalClean_RemovesMirroredAgentsMDBlock: global sync writes
// two managed regions into ~/.claude/CLAUDE.md, the @-import block and
// the AGENTS.md mirror. Clean removes both, so no mirrored instructions
// stay loaded after a clean, and keeps the user's own text.
func TestCmdGlobalClean_RemovesMirroredAgentsMDBlock(t *testing.T) {
	a, root, _ := newCleanTestApp(t)
	claudeMD := filepath.Join(root, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(claudeMD), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# My notes\n\nKeep me.\n\n" +
		AgentsMDBlockStart + "\n# Principles\nAlways verify.\n" + AgentsMDBlockEnd + "\n\n" +
		ManagedImportBlockStart + "\n@/rules/security.md\n" + ManagedImportBlockEnd + "\n"
	if err := os.WriteFile(claudeMD, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := a.CmdGlobalClean(GlobalCleanOpts{Targets: []string{"claude"}}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	data, err := os.ReadFile(claudeMD)
	if err != nil {
		t.Fatalf("CLAUDE.md with user text was removed: %v", err)
	}
	s := string(data)
	for _, gone := range []string{AgentsMDBlockStart, "Always verify.", ManagedImportBlockStart, "@/rules/security.md"} {
		if strings.Contains(s, gone) {
			t.Errorf("clean left %q in CLAUDE.md:\n%s", gone, s)
		}
	}
	if !strings.Contains(s, "Keep me.") {
		t.Errorf("clean removed user text:\n%s", s)
	}
}

// TestCmdGlobalClean_RemovesMirrorOnlyClaudeMD: a CLAUDE.md holding
// nothing but the AGENTS.md mirror is removed entirely by clean.
func TestCmdGlobalClean_RemovesMirrorOnlyClaudeMD(t *testing.T) {
	a, root, _ := newCleanTestApp(t)
	claudeMD := filepath.Join(root, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(claudeMD), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeMD, []byte(AgentsMDBlockStart+"\n# Principles\n"+AgentsMDBlockEnd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := a.CmdGlobalClean(GlobalCleanOpts{Targets: []string{"claude"}}); err != nil {
		t.Fatalf("clean: %v", err)
	}

	if _, err := os.Stat(claudeMD); !os.IsNotExist(err) {
		t.Errorf("mirror-only CLAUDE.md still present: %v", err)
	}
}
