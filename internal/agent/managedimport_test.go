package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegenerateClaudeImports_CreatesNewFile verifies the bootstrap
// case: when the target file doesn't exist, RegenerateClaudeImports
// creates it with a well-formed managed block.
func TestRegenerateClaudeImports_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")

	changed, err := RegenerateClaudeImports(target, []string{
		"/home/u/.claude/rules/security.md",
		"/home/u/.claude/rules/no-secrets.md",
	})
	if err != nil {
		t.Fatalf("RegenerateClaudeImports: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true on create")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, ManagedImportBlockStart) {
		t.Errorf("missing start marker:\n%s", content)
	}
	if !strings.Contains(content, ManagedImportBlockEnd) {
		t.Errorf("missing end marker:\n%s", content)
	}
	// Sorted alphabetically.
	if !strings.Contains(content, "@/home/u/.claude/rules/no-secrets.md") {
		t.Errorf("missing no-secrets import:\n%s", content)
	}
	if !strings.Contains(content, "@/home/u/.claude/rules/security.md") {
		t.Errorf("missing security import:\n%s", content)
	}
	// Ordering: no-secrets before security.
	noIdx := strings.Index(content, "@/home/u/.claude/rules/no-secrets.md")
	secIdx := strings.Index(content, "@/home/u/.claude/rules/security.md")
	if noIdx >= secIdx {
		t.Errorf("imports not sorted alphabetically:\n%s", content)
	}
}

// TestRegenerateClaudeImports_PreservesUserContent verifies that
// existing user content outside the markers survives a rewrite.
func TestRegenerateClaudeImports_PreservesUserContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")

	// Seed with user content + stale block.
	preexisting := "# My Claude config\n\nSome user prose.\n\n" +
		ManagedImportBlockStart + "\n" +
		"@/stale/path\n" +
		ManagedImportBlockEnd + "\n\n" +
		"More user prose after.\n"
	if err := os.WriteFile(target, []byte(preexisting), 0o644); err != nil {
		t.Fatalf("write pre: %v", err)
	}

	changed, err := RegenerateClaudeImports(target, []string{
		"/fresh/path.md",
	})
	if err != nil || !changed {
		t.Fatalf("regen: changed=%v err=%v", changed, err)
	}

	content, _ := os.ReadFile(target)
	s := string(content)

	if !strings.Contains(s, "# My Claude config") {
		t.Errorf("user content before marker stripped:\n%s", s)
	}
	if !strings.Contains(s, "Some user prose.") {
		t.Errorf("user content before marker stripped:\n%s", s)
	}
	if !strings.Contains(s, "More user prose after.") {
		t.Errorf("user content after marker stripped:\n%s", s)
	}
	if strings.Contains(s, "@/stale/path") {
		t.Errorf("stale import not replaced:\n%s", s)
	}
	if !strings.Contains(s, "@/fresh/path.md") {
		t.Errorf("fresh import not written:\n%s", s)
	}
}

// TestRegenerateClaudeImports_Idempotent verifies mtime preservation
// when the file content is byte-identical to what we'd write.
func TestRegenerateClaudeImports_Idempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")

	// First write: creates the file.
	if _, err := RegenerateClaudeImports(target, []string{"/a.md", "/b.md"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	info1, _ := os.Stat(target)

	// Second write: no-op.
	changed, err := RegenerateClaudeImports(target, []string{"/a.md", "/b.md"})
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if changed {
		t.Errorf("expected changed=false on idempotent re-run")
	}
	info2, _ := os.Stat(target)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Errorf("mtime changed on no-op write: %v -> %v", info1.ModTime(), info2.ModTime())
	}
}

// TestRegenerateClaudeImports_EmptyListEmitsEmptyBlock verifies the
// behavior when zero imports are supplied: the file contains the
// markers + banner but no @ lines.
func TestRegenerateClaudeImports_EmptyListEmitsEmptyBlock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CLAUDE.md")

	if _, err := RegenerateClaudeImports(target, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	content, _ := os.ReadFile(target)
	s := string(content)

	if !HasManagedImportBlock(s) {
		t.Errorf("block not written when empty:\n%s", s)
	}
	if strings.Contains(s, "@") {
		// The banner has no @, so this means we emitted an import.
		t.Errorf("unexpected @-import in empty block:\n%s", s)
	}
}

// TestHasManagedImportBlock covers the detection helper.
func TestHasManagedImportBlock(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"no markers", "just prose\n", false},
		{"start only", ManagedImportBlockStart + "\n", false},
		{"end only", ManagedImportBlockEnd + "\n", false},
		{"both markers", ManagedImportBlockStart + "\n@/a\n" + ManagedImportBlockEnd + "\n", true},
	}
	for _, c := range cases {
		if got := HasManagedImportBlock(c.in); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// TestExtractManagedImports verifies the extraction helper returns
// the @-lines with the @ prefix stripped and banner skipped.
func TestExtractManagedImports(t *testing.T) {
	content := "# User stuff\n\n" +
		ManagedImportBlockStart + "\n" +
		managedImportBanner + "\n" +
		"@/a.md\n" +
		"@/b.md\n" +
		ManagedImportBlockEnd + "\n"
	got := ExtractManagedImports(content)
	want := []string{"/a.md", "/b.md"}
	if len(got) != len(want) {
		t.Fatalf("got %d imports, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("import[%d]: got %q want %q", i, got[i], w)
		}
	}
}

// TestReplaceManagedBlock_NoMarkers verifies append-on-missing.
func TestReplaceManagedBlock_NoMarkers(t *testing.T) {
	existing := "# My file\n\nSome content.\n"
	got := replaceManagedBlock(existing, "NEWBLOCK\n")
	if !strings.Contains(got, "# My file") {
		t.Errorf("existing content lost:\n%s", got)
	}
	if !strings.Contains(got, "NEWBLOCK") {
		t.Errorf("block not appended:\n%s", got)
	}
}

// TestCollectClaudeRuleImportPaths verifies the filtering logic for
// which routed artifacts contribute to the @-import block.
func TestCollectClaudeRuleImportPaths(t *testing.T) {
	parent := "/home/u"
	arts := []ClaudeRoutedArtifact{
		{Type: ArtifactRule, Name: "security", Semantic: Passive},
		{Type: ArtifactWorkflow, Name: "review", Semantic: Passive},
		{Type: ArtifactRule, Name: "loud", Semantic: Invocable}, // shouldn't contribute
		{Type: ArtifactSkill, Name: "cool", Semantic: Passive},  // shouldn't contribute
	}
	got := CollectClaudeRuleImportPaths(parent, arts)
	// Returned in input order — sorted by the writer later.
	want := []string{
		"/home/u/.claude/rules/security.md",
		"/home/u/.claude/rules/review.md",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d imports, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("import[%d]: got %q want %q", i, got[i], w)
		}
	}
}

// TestManagedImportBlockForLocal verifies project-relative paths.
func TestManagedImportBlockForLocal(t *testing.T) {
	arts := []ClaudeRoutedArtifact{
		{Type: ArtifactRule, Name: "security", Semantic: Passive},
		{Type: ArtifactWorkflow, Name: "review", Semantic: Passive},
		{Type: ArtifactRule, Name: "shouty", Semantic: Invocable},
	}
	got := ManagedImportBlockForLocal(arts)
	// Returned in input order — sorted by the writer later.
	want := []string{".claude/rules/security.md", ".claude/rules/review.md"}
	if len(got) != len(want) {
		t.Fatalf("got %d imports, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("import[%d]: got %q want %q", i, got[i], w)
		}
	}
}

func TestResolveClaudeMDPath(t *testing.T) {
	tests := []struct {
		name      string
		parent    string
		hasAgents bool
		want      string
	}{
		{
			name:      "AGENTS.md takes precedence",
			parent:    "/project",
			hasAgents: true,
			want:      "/project/AGENTS.md",
		},
		{
			name:      "falls back to CLAUDE.md",
			parent:    "/project",
			hasAgents: false,
			want:      "/project/CLAUDE.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			projectRoot := filepath.Join(dir, "project")
			os.MkdirAll(projectRoot, 0o755)
			if tt.hasAgents {
				os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("test"), 0o644)
			}
			got := ResolveClaudeMDPath(projectRoot)
			if got != filepath.Join(projectRoot, "AGENTS.md") && got != filepath.Join(projectRoot, "CLAUDE.md") {
				t.Errorf("unexpected path: %s", got)
			}
			if tt.hasAgents && got != filepath.Join(projectRoot, "AGENTS.md") {
				t.Errorf("wanted AGENTS.md, got %s", got)
			}
			if !tt.hasAgents && got != filepath.Join(projectRoot, "CLAUDE.md") {
				t.Errorf("wanted CLAUDE.md, got %s", got)
			}
		})
	}
}

func TestResolveClaudeMDPath_AgentsTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	projectRoot := filepath.Join(dir, "project")
	os.MkdirAll(projectRoot, 0o755)
	os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("test"), 0o644)
	os.WriteFile(filepath.Join(projectRoot, "CLAUDE.md"), []byte("test"), 0o644)

	got := ResolveClaudeMDPath(projectRoot)
	if got != filepath.Join(projectRoot, "AGENTS.md") {
		t.Errorf("AGENTS.md should take precedence, got %s", got)
	}
}
