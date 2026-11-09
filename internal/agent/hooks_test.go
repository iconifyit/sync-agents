package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// --- hookEntriesEqual ---

func TestHookEntriesEqual(t *testing.T) {
	a := hookEntry{Hooks: []map[string]any{{"type": "command", "command": "x"}}}
	b := hookEntry{Hooks: []map[string]any{{"type": "command", "command": "x"}}}
	c := hookEntry{Hooks: []map[string]any{{"type": "command", "command": "y"}}}
	d := hookEntry{Matcher: "Bash", Hooks: []map[string]any{{"type": "command", "command": "x"}}}
	if !hookEntriesEqual(a, b) {
		t.Error("identical entries should be equal")
	}
	if hookEntriesEqual(a, c) {
		t.Error("different commands should not be equal")
	}
	if hookEntriesEqual(a, d) {
		t.Error("different matchers should not be equal")
	}
}

// --- fileExists / dirExists ---

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	if fileExists(filepath.Join(dir, "nope")) {
		t.Error("non-existent file should return false")
	}
	f, _ := os.Create(filepath.Join(dir, "yes"))
	f.Close()
	if !fileExists(filepath.Join(dir, "yes")) {
		t.Error("existing file should return true")
	}
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()
	if dirExists(filepath.Join(dir, "nope")) {
		t.Error("non-existent dir should return false")
	}
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	if !dirExists(filepath.Join(dir, "sub")) {
		t.Error("existing dir should return true")
	}
}

// --- jsonMarshalSorted ---

func TestJsonMarshalSorted(t *testing.T) {
	m := map[string]json.RawMessage{
		"b": json.RawMessage(`2`),
		"a": json.RawMessage(`1`),
		"c": json.RawMessage(`{"nested":true}`),
	}
	out, err := jsonMarshalSorted(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		t.Errorf("not valid JSON: %s", s)
	}
	if strings.Index(s, `"a"`) > strings.Index(s, `"b"`) {
		t.Errorf("keys not sorted: %s", s)
	}
	if strings.Index(s, `"b"`) > strings.Index(s, `"c"`) {
		t.Errorf("keys not sorted: %s", s)
	}
}

func TestJsonMarshalSorted_Empty(t *testing.T) {
	out, err := jsonMarshalSorted(map[string]json.RawMessage{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "{}" {
		t.Errorf("expected {}, got %s", out)
	}
}

// --- readSettingsJSON ---

func TestReadSettingsJSON_Empty(t *testing.T) {
	s, raw, err := readSettingsJSON(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected non-nil settings")
	}
	if raw != nil {
		t.Error("expected nil raw for missing file")
	}
}

func TestReadSettingsJSON_WithHooks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"x"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, raw, err := readSettingsJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Hooks) != 1 {
		t.Fatalf("expected 1 hook event, got %d", len(s.Hooks))
	}
	if raw == nil {
		t.Error("expected raw map")
	}
}

func TestReadSettingsJSON_Malformed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	os.WriteFile(p, []byte(`not json`), 0o644)
	s, _, err := readSettingsJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Hooks) != 0 {
		t.Error("expected empty hooks for malformed file")
	}
}

func TestReadSettingsJSON_HooksWrongType(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	os.WriteFile(p, []byte(`{"hooks":"not an object"}`), 0o644)
	s, raw, err := readSettingsJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Hooks) != 0 {
		t.Error("expected empty hooks when hooks is wrong type")
	}
	if raw == nil {
		t.Error("expected raw map to be preserved")
	}
	if _, ok := raw["hooks"]; !ok {
		t.Error("expected hooks key in raw")
	}
}

// --- readHooksState ---

func TestReadHooksState_Missing(t *testing.T) {
	s, err := readHooksState(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 0 {
		t.Errorf("expected 0 entries for missing file, got %d", len(s.Entries))
	}
}

func TestReadHooksState_Valid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	os.WriteFile(p, []byte(`{"entries":{"lint":{"event":"PreToolUse","sha":"abc"}},"syncAgentsCreated":true}`), 0o644)
	s, err := readHooksState(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(s.Entries))
	}
	if s.Entries["lint"].Event != "PreToolUse" {
		t.Errorf("wrong event: %s", s.Entries["lint"].Event)
	}
	if !s.SyncAgentsCreated {
		t.Error("expected syncAgentsCreated=true")
	}
}

func TestReadHooksState_Malformed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	os.WriteFile(p, []byte(`garbage`), 0o644)
	s, err := readHooksState(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 0 {
		t.Errorf("expected 0 entries for malformed, got %d", len(s.Entries))
	}
}

// --- writeHooksState round-trip ---

func TestWriteReadHooksState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")

	hooksState := hooksState{
		Entries: map[string]hooksStateEntry{
			"lint":   {Event: "PreToolUse", SHA: "abc123"},
			"notify": {Event: "Notification", SHA: "def456"},
		},
	}
	if err := writeHooksState(p, hooksState); err != nil {
		t.Fatal(err)
	}

	s, err := readHooksState(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(s.Entries))
	}
	if s.Entries["lint"].Event != "PreToolUse" || s.Entries["lint"].SHA != "abc123" {
		t.Errorf("lint entry mismatch: %+v", s.Entries["lint"])
	}
	if !s.SyncAgentsCreated {
		t.Error("expected syncAgentsCreated=true")
	}
}

// --- writeSettingsJSON ---

func TestWriteSettingsJSON_HooksOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")

	settings := &claudeSettings{
		Hooks: map[string][]hookEntry{
			"PreToolUse": {{Hooks: []map[string]any{{"type": "command", "command": "x"}}}},
		},
	}
	if err := writeSettingsJSON(p, settings, nil, true); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(p)
	var out map[string]json.RawMessage
	json.Unmarshal(data, &out)
	if _, ok := out["hooks"]; !ok {
		t.Error("hooks key missing")
	}
	if _, ok := out["hooks"]; ok {
		var hooks map[string][]hookEntry
		json.Unmarshal(out["hooks"], &hooks)
		if len(hooks) != 1 {
			t.Errorf("expected 1 event, got %d", len(hooks))
		}
	}
}

func TestWriteSettingsJSON_PreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")

	raw := map[string]json.RawMessage{
		"permissions": json.RawMessage(`{"allow":["Bash"]}`),
	}
	settings := &claudeSettings{
		Hooks: map[string][]hookEntry{
			"Stop": {{Hooks: []map[string]any{{"type": "command", "command": "echo done"}}}},
		},
	}
	if err := writeSettingsJSON(p, settings, raw, false); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(p)
	s := string(data)
	// Keys sorted alphabetically: hooks before permissions
	if !strings.Contains(s, `"hooks"`) || !strings.Contains(s, `"permissions"`) {
		t.Errorf("output missing keys:\n%s", s)
	}
	// Permissions key preserved
	if !strings.Contains(s, `"allow":["Bash"]`) {
		t.Errorf("permissions lost:\n%s", s)
	}
}

func TestWriteSettingsJSON_EmptyHooksRemoved(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")

	raw := map[string]json.RawMessage{
		"hooks": json.RawMessage(`{}`),
	}
	settings := &claudeSettings{} // no hooks
	if err := writeSettingsJSON(p, settings, raw, false); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "{}") || strings.Contains(string(data), "hooks") {
		t.Errorf("expected empty object, got: %s", data)
	}
}

// --- findHookOwner ---

func TestFindHookOwner(t *testing.T) {
	app := &App{}
	state := &hooksStateInternal{
		Entries: map[string]hooksStateEntry{
			"lint": {Event: "PreToolUse", SHA: "sha-lint"},
		},
	}
	entry := hookEntry{Hooks: []map[string]any{{"type": "command", "command": "echo lint"}}}

	// findHookOwner computes its own SHA from entry.Hooks.
	// We seeded lint with SHA "sha-lint" which won't match.
	owner := app.findHookOwner("PreToolUse", entry, state)
	if owner != "" {
		t.Errorf("expected no owner (SHA mismatch), got %q", owner)
	}

	// Now seed with the correct SHA.
	correctSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%v", entry.Hooks))))
	state.Entries["lint"] = hooksStateEntry{Event: "PreToolUse", SHA: correctSHA}
	owner = app.findHookOwner("PreToolUse", entry, state)
	if owner != "lint" {
		t.Errorf("expected owner 'lint', got %q", owner)
	}

	// Wrong event.
	owner = app.findHookOwner("Stop", entry, state)
	if owner != "" {
		t.Errorf("expected no owner (wrong event), got %q", owner)
	}
}

// --- MergeHooks ---

func TestMergeHooks_CreateNew(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "lint.json"),
		[]byte(`{"event":"PreToolUse","matcher":"Bash","hooks":[{"type":"command","command":"echo lint"}]}`),
		0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	settingsPath := filepath.Join(dir, "settings.json")
	statePath := filepath.Join(dir, "state.json")

	n, err := app.MergeHooks(hooksDir, settingsPath, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 hook merged, got %d", n)
	}

	// settings.json exists
	data, _ := os.ReadFile(settingsPath)
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("invalid settings.json: %v\n%s", err, data)
	}
	if _, ok := out["hooks"]; !ok {
		t.Fatal("hooks key missing")
	}
	var h map[string][]hookEntry
	json.Unmarshal(out["hooks"], &h)
	if len(h["PreToolUse"]) != 1 {
		t.Fatalf("expected 1 PreToolUse entry, got %d", len(h["PreToolUse"]))
	}
	if h["PreToolUse"][0].Matcher != "Bash" {
		t.Errorf("matcher not preserved: %+v", h["PreToolUse"][0])
	}

	// state file
	if _, err := os.Stat(statePath); err != nil {
		t.Fatal("state file not created")
	}
}

func TestMergeHooks_Idempotent(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "lint.json"),
		[]byte(`{"event":"PreToolUse","hooks":[{"type":"command","command":"x"}]}`),
		0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	settingsPath := filepath.Join(dir, "settings.json")
	statePath := filepath.Join(dir, "state.json")

	// First merge.
	n1, _ := app.MergeHooks(hooksDir, settingsPath, statePath)
	firstData, _ := os.ReadFile(settingsPath)

	// Second merge — should be idempotent.
	n2, _ := app.MergeHooks(hooksDir, settingsPath, statePath)
	secondData, _ := os.ReadFile(settingsPath)

	if n1 != 1 || n2 != 1 {
		t.Errorf("n1=%d n2=%d", n1, n2)
	}
	if string(firstData) != string(secondData) {
		t.Errorf("settings.json changed on idempotent re-merge:\n1: %s\n2: %s", firstData, secondData)
	}

	// Duplicate hook shouldn't appear.
	var out map[string]json.RawMessage
	json.Unmarshal(secondData, &out)
	var h map[string][]hookEntry
	json.Unmarshal(out["hooks"], &h)
	if len(h["PreToolUse"]) != 1 {
		t.Errorf("expected 1 entry (no dup), got %d", len(h["PreToolUse"]))
	}
}

func TestMergeHooks_PreservesUserContent(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "lint.json"),
		[]byte(`{"event":"PreToolUse","hooks":[{"type":"command","command":"echo lint"}]}`),
		0o644)

	// Pre-seed settings.json with user content.
	settingsPath := filepath.Join(dir, "settings.json")
	os.WriteFile(settingsPath,
		[]byte(`{"permissions":{"allow":["Bash"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user-hook"}]}]}}`),
		0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	statePath := filepath.Join(dir, "state.json")

	_, err := app.MergeHooks(hooksDir, settingsPath, statePath)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(settingsPath)
	s := string(data)

	// User's Stop hook preserved.
	if !strings.Contains(s, "user-hook") {
		t.Errorf("user hook lost:\n%s", s)
	}
	// User's permissions preserved.
	if !strings.Contains(s, "permissions") {
		t.Errorf("permissions lost:\n%s", s)
	}
	// Our PreToolUse hook added.
	if !strings.Contains(s, "echo lint") || !strings.Contains(s, "PreToolUse") {
		t.Errorf("synced hook missing:\n%s", s)
	}
}

func TestMergeHooks_StaleRemoval(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	os.MkdirAll(hooksDir, 0o755)

	// First, create a hook and merge it.
	os.WriteFile(filepath.Join(hooksDir, "stale.json"),
		[]byte(`{"event":"PreToolUse","hooks":[{"type":"command","command":"stale"}]}`),
		0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	settingsPath := filepath.Join(dir, "settings.json")
	statePath := filepath.Join(dir, "state.json")

	app.MergeHooks(hooksDir, settingsPath, statePath)

	// Now delete the hook file and add a different one.
	os.Remove(filepath.Join(hooksDir, "stale.json"))
	os.WriteFile(filepath.Join(hooksDir, "fresh.json"),
		[]byte(`{"event":"Stop","hooks":[{"type":"command","command":"fresh"}]}`),
		0o644)

	n, err := app.MergeHooks(hooksDir, settingsPath, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 hook merged, got %d", n)
	}

	data, _ := os.ReadFile(settingsPath)
	s := string(data)
	if strings.Contains(s, "stale") {
		t.Errorf("stale hook not removed:\n%s", s)
	}
	if !strings.Contains(s, "fresh") {
		t.Errorf("fresh hook missing:\n%s", s)
	}
}

func TestMergeHooks_EmptyHooksDir(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "nonexistent")
	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	n, err := app.MergeHooks(hooksDir, filepath.Join(dir, "settings.json"), filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 hooks for empty dir, got %d", n)
	}
}

func TestMergeHooks_SkipsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "bad.json"), []byte(`not json`), 0o644)
	os.WriteFile(filepath.Join(hooksDir, "good.json"),
		[]byte(`{"event":"Stop","hooks":[{"type":"command","command":"ok"}]}`), 0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	settingsPath := filepath.Join(dir, "settings.json")
	statePath := filepath.Join(dir, "state.json")

	n, err := app.MergeHooks(hooksDir, settingsPath, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 good hook merged, got %d", n)
	}
}

func TestMergeHooks_MissingEvent(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "noevent.json"),
		[]byte(`{"hooks":[{"type":"command","command":"x"}]}`), 0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	n, _ := app.MergeHooks(hooksDir, filepath.Join(dir, "settings.json"), filepath.Join(dir, "state.json"))
	if n != 0 {
		t.Errorf("expected 0 hooks (missing event), got %d", n)
	}
}

// --- CleanHooks ---

func TestCleanHooks_RemovesOwnedEntries(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "lint.json"),
		[]byte(`{"event":"PreToolUse","hooks":[{"type":"command","command":"echo lint"}]}`),
		0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	settingsPath := filepath.Join(dir, "settings.json")
	statePath := filepath.Join(dir, "state.json")

	// Merge then clean.
	app.MergeHooks(hooksDir, settingsPath, statePath)

	n, err := app.CleanHooks(settingsPath, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 entry removed, got %d", n)
	}

	// settings.json should be gone (no user content, sync-agents created).
	if _, err := os.Stat(settingsPath); err == nil {
		data, _ := os.ReadFile(settingsPath)
		t.Errorf("settings.json should be removed, got: %s", data)
	}
	// State file should be gone.
	if _, err := os.Stat(statePath); err == nil {
		t.Error("state file should be removed")
	}
}

func TestCleanHooks_PreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	os.MkdirAll(hooksDir, 0o755)
	os.WriteFile(filepath.Join(hooksDir, "lint.json"),
		[]byte(`{"event":"PreToolUse","hooks":[{"type":"command","command":"sync-hook"}]}`),
		0o644)

	// Pre-seed user content.
	settingsPath := filepath.Join(dir, "settings.json")
	os.WriteFile(settingsPath,
		[]byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user-hook"}]}]},"permissions":{"allow":["Bash"]}}`),
		0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	statePath := filepath.Join(dir, "state.json")
	app.MergeHooks(hooksDir, settingsPath, statePath)

	// Clean.
	n, _ := app.CleanHooks(settingsPath, statePath)
	if n != 1 {
		t.Errorf("expected 1 entry removed, got %d", n)
	}

	data, _ := os.ReadFile(settingsPath)
	s := string(data)

	if strings.Contains(s, "sync-hook") {
		t.Errorf("synced hook not removed:\n%s", s)
	}
	if !strings.Contains(s, "user-hook") {
		t.Errorf("user hook lost:\n%s", s)
	}
	if !strings.Contains(s, "permissions") {
		t.Errorf("permissions lost:\n%s", s)
	}
}

func TestCleanHooks_NoStateNoop(t *testing.T) {
	dir := t.TempDir()
	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	n, err := app.CleanHooks(filepath.Join(dir, "settings.json"), filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

// --- readHookFragments ---

func TestReadHookFragments_FiltersNonJSON(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("not json"), 0o644)
	os.WriteFile(filepath.Join(dir, "lint.json"),
		[]byte(`{"event":"Stop","hooks":[{"type":"command","command":"x"}]}`), 0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	frags, err := app.readHookFragments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 {
		t.Errorf("expected 1 fragment (filtered .md), got %d", len(frags))
	}
	if frags[0].Name != "lint" {
		t.Errorf("expected name 'lint', got %q", frags[0].Name)
	}
}

func TestReadHookFragments_SortedByName(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "c.json"),
		[]byte(`{"event":"Stop","hooks":[{"type":"command","command":"c"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "a.json"),
		[]byte(`{"event":"Stop","hooks":[{"type":"command","command":"a"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.json"),
		[]byte(`{"event":"Stop","hooks":[{"type":"command","command":"b"}]}`), 0o644)

	app := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	frags, _ := app.readHookFragments(dir)
	if len(frags) != 3 {
		t.Fatalf("expected 3, got %d", len(frags))
	}
	names := make([]string, len(frags))
	for i, f := range frags {
		names[i] = f.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("not sorted: %v", names)
	}
}
