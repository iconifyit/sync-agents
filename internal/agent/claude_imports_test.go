package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildClaudeImportBlock_Empty verifies the empty-paths case
// produces a block with just markers. This matters because a sync
// against an empty rules dir must still write a well-formed block
// (future syncs need the markers to find their injection point).
func TestBuildClaudeImportBlock_Empty(t *testing.T) {
	got := BuildClaudeImportBlock(nil)
	want := ClaudeImportsBlockStart + "\n" + ClaudeImportsBlockEnd
	if got != want {
		t.Errorf("empty paths:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestBuildClaudeImportBlock_Sorted verifies output is sorted
// regardless of input order. Deterministic block bytes mean
// reruns over the same rules dir don't dirty the file with
// noise from unstable iteration order.
func TestBuildClaudeImportBlock_Sorted(t *testing.T) {
	paths := []string{"/x/zebra.md", "/x/apple.md", "/x/mango.md"}
	got := BuildClaudeImportBlock(paths)
	want := strings.Join([]string{
		ClaudeImportsBlockStart,
		"@/x/apple.md",
		"@/x/mango.md",
		"@/x/zebra.md",
		ClaudeImportsBlockEnd,
	}, "\n")
	if got != want {
		t.Errorf("\ngot:  %q\nwant: %q", got, want)
	}
}

// TestBuildClaudeImportBlock_DoesNotMutateInput proves the function
// sorts a copy, never the caller's slice. Defends against future
// surprise where reusing the slice in the caller produces drift.
func TestBuildClaudeImportBlock_DoesNotMutateInput(t *testing.T) {
	paths := []string{"/x/zebra.md", "/x/apple.md"}
	original := append([]string(nil), paths...)
	_ = BuildClaudeImportBlock(paths)
	for i := range original {
		if paths[i] != original[i] {
			t.Errorf("input mutated at index %d: got %q, want %q", i, paths[i], original[i])
		}
	}
}

// TestUpsertClaudeImportBlock covers the three structural cases
// (empty / no-block / has-block) with one table. The exact wanted
// bytes are spelled out so any future regression in whitespace
// handling is visible in the diff.
func TestUpsertClaudeImportBlock(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		paths    []string
		want     string
	}{
		{
			name:     "empty file",
			existing: "",
			paths:    []string{"/a.md"},
			want:     ClaudeImportsBlockStart + "\n@/a.md\n" + ClaudeImportsBlockEnd + "\n",
		},
		{
			name:     "existing content, no block",
			existing: "# My rules\n\nSome prose.\n",
			paths:    []string{"/a.md"},
			want: "# My rules\n\nSome prose.\n\n" +
				ClaudeImportsBlockStart + "\n@/a.md\n" + ClaudeImportsBlockEnd + "\n",
		},
		{
			name: "replaces existing block, preserves surrounding content",
			existing: "# My rules\n\n" +
				ClaudeImportsBlockStart + "\n@/old.md\n" + ClaudeImportsBlockEnd +
				"\n\nAfter the block.\n",
			paths: []string{"/new.md"},
			want: "# My rules\n\nAfter the block.\n\n" +
				ClaudeImportsBlockStart + "\n@/new.md\n" + ClaudeImportsBlockEnd + "\n",
		},
		{
			name: "block with no surrounding content",
			existing: ClaudeImportsBlockStart + "\n@/old.md\n" + ClaudeImportsBlockEnd + "\n",
			paths:    []string{"/new.md"},
			want:     ClaudeImportsBlockStart + "\n@/new.md\n" + ClaudeImportsBlockEnd + "\n",
		},
		{
			name:     "multiple paths land sorted in the block",
			existing: "",
			paths:    []string{"/z.md", "/a.md", "/m.md"},
			want: ClaudeImportsBlockStart + "\n@/a.md\n@/m.md\n@/z.md\n" +
				ClaudeImportsBlockEnd + "\n",
		},
		{
			name:     "empty rules but existing block: block reduced to markers only",
			existing: ClaudeImportsBlockStart + "\n@/old.md\n" + ClaudeImportsBlockEnd + "\n",
			paths:    nil,
			want:     ClaudeImportsBlockStart + "\n" + ClaudeImportsBlockEnd + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := BuildClaudeImportBlock(tt.paths)
			got := UpsertClaudeImportBlock(tt.existing, block)
			if got != tt.want {
				t.Errorf("\ngot:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

// TestRemoveClaudeImportBlock_Strips proves removal preserves
// surrounding content and collapses redundant blank lines.
func TestRemoveClaudeImportBlock_Strips(t *testing.T) {
	existing := "Top\n\n" +
		ClaudeImportsBlockStart + "\n@/a.md\n" + ClaudeImportsBlockEnd +
		"\n\nBottom\n"
	got := RemoveClaudeImportBlock(existing)
	want := "Top\n\nBottom\n"
	if got != want {
		t.Errorf("\ngot:  %q\nwant: %q", got, want)
	}
}

// TestRemoveClaudeImportBlock_AbsentIsNoOp proves the function is
// a pass-through when the block isn't present. Removing a block
// from a file that never had one must produce zero bytes of
// difference.
func TestRemoveClaudeImportBlock_AbsentIsNoOp(t *testing.T) {
	existing := "Top\n\nBottom\n"
	got := RemoveClaudeImportBlock(existing)
	if got != existing {
		t.Errorf("\ngot:  %q\nwant: %q (unchanged)", got, existing)
	}
}

// TestWriteClaudeImportsBlock_CreatesFileAndParentDirs proves the
// writer handles the missing-file case: file is created, parent
// dirs are created, content is correct.
func TestWriteClaudeImportsBlock_CreatesFileAndParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing-subdir", "CLAUDE.md")

	changed, err := WriteClaudeImportsBlock(path, []string{"/x/a.md"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !changed {
		t.Errorf("creating new file should report changed=true")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "@/x/a.md") {
		t.Errorf("written content missing import line: %q", string(data))
	}
	if !strings.Contains(string(data), ClaudeImportsBlockStart) ||
		!strings.Contains(string(data), ClaudeImportsBlockEnd) {
		t.Errorf("written content missing markers: %q", string(data))
	}
}

// TestWriteClaudeImportsBlock_Idempotent proves the second write
// with identical inputs reports changed=false and doesn't dirty
// the on-disk byte content — the key property for "no spurious
// rewrites" in repeated syncs.
func TestWriteClaudeImportsBlock_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte("# My CLAUDE.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed1, err := WriteClaudeImportsBlock(path, []string{"/x/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed1 {
		t.Errorf("first call should report changed=true")
	}
	bytes1, _ := os.ReadFile(path)

	changed2, err := WriteClaudeImportsBlock(path, []string{"/x/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Errorf("second call with same paths should report changed=false")
	}
	bytes2, _ := os.ReadFile(path)
	if string(bytes1) != string(bytes2) {
		t.Errorf("on-disk content drifted between idempotent calls:\nfirst:  %q\nsecond: %q",
			string(bytes1), string(bytes2))
	}
}

// TestWriteClaudeImportsBlock_PreservesPriorContent proves the writer
// never modifies bytes outside the managed markers. Catches future
// regressions where someone "helpfully" normalizes the whole file.
func TestWriteClaudeImportsBlock_PreservesPriorContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	prior := "# Project notes\n\nA section the user wrote.\n\nMore prose.\n"
	if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteClaudeImportsBlock(path, []string{"/x/a.md"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# Project notes") ||
		!strings.Contains(string(data), "A section the user wrote.") ||
		!strings.Contains(string(data), "More prose.") {
		t.Errorf("prior content was modified or dropped:\n%q", string(data))
	}
}
