package agent

import (
	"path/filepath"
	"testing"
)

// TestResolveGlobalRoot_FieldPrecedence asserts that App.GlobalRoot,
// when set, wins over both the env var and the $HOME default. This
// is the topmost level of the precedence chain documented in
// docs/architecture/global-root-resolution.md.
func TestResolveGlobalRoot_FieldPrecedence(t *testing.T) {
	t.Setenv(EnvGlobalRoot, "/from/env/.agents")

	a := &App{GlobalRoot: "/from/field/.agents"}
	got := a.ResolveGlobalRoot()
	want := "/from/field/.agents"
	if got != want {
		t.Errorf("ResolveGlobalRoot() = %q, want %q (field must win over env)", got, want)
	}
}

// TestResolveGlobalRoot_EnvBeatsHome asserts level 2 of the chain:
// when the field is empty and the env var is set, the env var wins
// over $HOME/.agents.
func TestResolveGlobalRoot_EnvBeatsHome(t *testing.T) {
	t.Setenv(EnvGlobalRoot, "/from/env/.agents")
	// Even if HOME points somewhere, env should win.
	t.Setenv("HOME", "/some/home")

	a := &App{}
	got := a.ResolveGlobalRoot()
	want := "/from/env/.agents"
	if got != want {
		t.Errorf("ResolveGlobalRoot() = %q, want %q (env must win over HOME default)", got, want)
	}
}

// TestResolveGlobalRoot_HomeDefault asserts the bottom of the chain:
// no field, no env var, returns $HOME/.agents.
func TestResolveGlobalRoot_HomeDefault(t *testing.T) {
	t.Setenv(EnvGlobalRoot, "")
	t.Setenv("HOME", "/some/home")

	a := &App{}
	got := a.ResolveGlobalRoot()
	want := filepath.Join("/some/home", DefaultGlobalRootSegment)
	if got != want {
		t.Errorf("ResolveGlobalRoot() = %q, want %q", got, want)
	}
}

// TestResolveGlobalRoot_AbsolutePathNormalization confirms relative
// paths get expanded. The contract is "return an absolute path"; this
// matters for tests that pass --global-root=./tmp-root and for the
// resilience of downstream symlink/write operations.
func TestResolveGlobalRoot_AbsolutePathNormalization(t *testing.T) {
	a := &App{GlobalRoot: "./relative/.agents"}
	got := a.ResolveGlobalRoot()
	if !filepath.IsAbs(got) {
		t.Errorf("ResolveGlobalRoot() = %q is not absolute; relative inputs must be normalized", got)
	}
}

// TestResolveGlobalRootParent confirms the derivation rule: the
// parent is filepath.Dir of the resolved root. This is the path
// every per-tool global dir is joined against.
func TestResolveGlobalRootParent(t *testing.T) {
	a := &App{GlobalRoot: "/tmp/g/.agents"}
	got := a.ResolveGlobalRootParent()
	want := "/tmp/g"
	if got != want {
		t.Errorf("ResolveGlobalRootParent() = %q, want %q", got, want)
	}
}

// TestResolveToolDir_Local covers the local-scope path: tool dir
// joined against ProjectRoot. This is the common case and the
// regression risk if anyone hand-builds these paths elsewhere.
func TestResolveToolDir_Local(t *testing.T) {
	a := &App{ProjectRoot: "/proj"}
	claude, _ := ResolveTool("claude")
	got := a.ResolveToolDir(claude, ScopeLocal)
	want := filepath.Join("/proj", ".claude")
	if got != want {
		t.Errorf("ResolveToolDir(claude, local) = %q, want %q", got, want)
	}
}

// TestResolveToolDir_GlobalCodeiumAsymmetry exercises the
// windsurf/codeium asymmetry end-to-end: from the App's global root
// through Tool.DirForScope. A green test here means the asymmetry is
// invisible to call sites that go through ResolveToolDir.
func TestResolveToolDir_GlobalCodeiumAsymmetry(t *testing.T) {
	a := &App{GlobalRoot: "/tmp/g/.agents"}
	codeium, _ := ResolveTool("codeium")

	got := a.ResolveToolDir(codeium, ScopeGlobal)
	want := filepath.Join("/tmp/g", ".codeium")
	if got != want {
		t.Errorf("ResolveToolDir(codeium, global) = %q, want %q", got, want)
	}

	// And the local form still resolves to .windsurf rooted at
	// ProjectRoot, not the global parent.
	a.ProjectRoot = "/proj"
	got = a.ResolveToolDir(codeium, ScopeLocal)
	want = filepath.Join("/proj", ".windsurf")
	if got != want {
		t.Errorf("ResolveToolDir(codeium, local) = %q, want %q", got, want)
	}
}

// TestResolveToolDir_EmptyWhenNoMapping documents the LocalOnly
// opt-out: a tool with no global mapping returns the empty string,
// and callers are expected to detect that via Tool.HasScope.
func TestResolveToolDir_EmptyWhenNoMapping(t *testing.T) {
	a := &App{ProjectRoot: "/proj", GlobalRoot: "/tmp/g/.agents"}
	// Synthesize a LocalOnly tool inline; we don't put it in the
	// registry because that would change the public surface.
	tool := Tool{
		ID:         "synthetic",
		DirByScope: map[Scope]string{ScopeLocal: ".synthetic"},
		LocalOnly:  true,
	}
	if got := a.ResolveToolDir(tool, ScopeGlobal); got != "" {
		t.Errorf("ResolveToolDir(LocalOnly, ScopeGlobal) = %q, want empty string", got)
	}
}
