package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// hooks.go implements SPEC-004 Part C: merging .agents/hooks/*.json
// fragments into .claude/settings.json with strict ownership tracking.
//
// The model:
//   - Each .agents/hooks/<name>.json is one Claude hook fragment.
//   - On sync, all fragments are collected and merged into the
//     destination settings.json.
//   - Ownership is recorded in .agents/.sync/claude-hooks-state.json:
//     which fragments sync-agents wrote, their events, and content
//     hashes. This lets clean remove precisely what sync-agents
//     added without touching user-authored hook entries or other
//     settings keys.
//   - On re-sync, stale entries (previously written by sync-agents
//     but no longer present in .agents/hooks/) are removed from
//     settings.json before the current set is written.
//   - Atomic writes via tmp + rename; key-order stable for
//     reviewable diffs; idempotent (sync twice = identical output).

// HookFragment is the user-authored hook JSON shape under
// .agents/hooks/. The "event" and "hooks" keys map directly to
// the Claude settings.json schema; matcher is optional.
type HookFragment struct {
	// Name is the stem of the source file (no extension) — used
	// as the ownership key in the state file.
	Name string `json:"-"`

	// Event is the Claude hook event: "PreToolUse", "PostToolUse",
	// "Notification", "Stop", "SubagentStop", "PreCompact",
	// "UserPromptSubmit".
	Event string `json:"event"`

	// Matcher is an optional tool matcher (e.g. "Bash", "Edit").
	// Omitted from settings.json when empty.
	Matcher string `json:"matcher,omitempty"`

	// Hooks is the array of hook actions — typically one entry
	// with type "command" and a command string.
	Hooks []map[string]any `json:"hooks"`
}

// claudeSettings is the subset of .claude/settings.json that
// sync-agents writes. Other top-level keys pass through untouched.
type claudeSettings struct {
	Hooks map[string][]hookEntry `json:"hooks,omitempty"`
}

// hookEntry is one entry in settings.json hooks.<event>[].
type hookEntry struct {
	Matcher string           `json:"matcher,omitempty"`
	Hooks   []map[string]any `json:"hooks"`
}

// hooksState records sync-agents' ownership of entries written into
// .claude/settings.json. Written to .agents/.sync/claude-hooks-state.json.
type hooksState struct {
	// Entries maps hook fragment name → the event + content hash
	// of the last version sync-agents wrote.
	Entries map[string]hooksStateEntry `json:"entries"`
}

type hooksStateEntry struct {
	Event string `json:"event"`
	SHA   string `json:"sha"`
}

// MergeHooks collects every .agents/hooks/*.json fragment, merges
// them into .claude/settings.json with ownership tracking, and
// writes the updated state file. Returns the number of hook
// fragments processed (0 when hooksDir is missing or empty).
//
// Existing user-authored hooks and non-hooks settings keys pass
// through untouched. Sync-agents removes only entries it previously
// wrote (tracked by state) that no longer have a corresponding
// fragment.
func (a *App) MergeHooks(hooksDir, claudeSettingsPath, statePath string) (int, error) {
	// Read current fragments.
	frags, err := a.readHookFragments(hooksDir)
	if err != nil {
		return 0, err
	}
	if len(frags) == 0 && !fileExists(statePath) && !fileExists(claudeSettingsPath) {
		return 0, nil // nothing to merge and nothing to clean
	}

	// Read current settings.json.
	settings, rawSettings, err := readSettingsJSON(claudeSettingsPath)
	if err != nil {
		return 0, err
	}

	// Read ownership state.
	state, err := readHooksState(statePath)
	if err != nil {
		return 0, err
	}

	// Remove stale entries: entries sync-agents wrote last time that
	// no longer have a matching fragment.
	newFragNames := make(map[string]bool, len(frags))
	for _, f := range frags {
		newFragNames[f.Name] = true
	}
	if settings.Hooks != nil {
		for event := range settings.Hooks {
			filtered := make([]hookEntry, 0, len(settings.Hooks[event]))
			for i, entry := range settings.Hooks[event] {
				// Determine if this entry is ours. We find the owning
				// fragment by matching against state.
				owner := a.findHookOwner(event, entry, state)
				if owner != "" && !newFragNames[owner] {
					// Stale — owned by sync-agents but the fragment
					// was deleted from .agents/hooks/.
					a.Info(fmt.Sprintf("hooks: removing stale entry from event %q (was %q)", event, owner))
					continue
				}
				_ = i
				filtered = append(filtered, entry)
			}
			if len(filtered) == 0 {
				delete(settings.Hooks, event)
			} else {
				settings.Hooks[event] = filtered
			}
		}
	}

	// Add current fragments. Each fragment is one hookEntry under
	// its declared event. If a fragment with the same name was
	// previously written under a different event, the stale removal
	// above (by name match) handled that. Here we just add.
	added := 0
	newState := hooksState{Entries: make(map[string]hooksStateEntry, len(frags))}
	for _, f := range frags {
		sha := sha256.Sum256([]byte(fmt.Sprintf("%v", f.Hooks)))
		shaHex := fmt.Sprintf("%x", sha)
		newState.Entries[f.Name] = hooksStateEntry{Event: f.Event, SHA: shaHex}
		if settings.Hooks == nil {
			settings.Hooks = make(map[string][]hookEntry)
		}
		entry := hookEntry{Hooks: f.Hooks}
		if f.Matcher != "" {
			entry.Matcher = f.Matcher
		}
		// Deduplicate: skip if an identical entry already exists
		// for this event (idempotent re-sync).
		dup := false
		for _, existing := range settings.Hooks[f.Event] {
			if hookEntriesEqual(existing, entry) {
				dup = true
				break
			}
		}
		if !dup {
			settings.Hooks[f.Event] = append(settings.Hooks[f.Event], entry)
			added++
		}
	}

	// If no hooks remain, delete hooks key entirely.
	if len(settings.Hooks) == 0 {
		settings.Hooks = nil
	}

	// Decide whether to write or remove settings.json.
	hasContent := settings.Hooks != nil
	hasOtherKeys := false
	if rawSettings != nil {
		for k := range rawSettings {
			if k != "hooks" {
				hasOtherKeys = true
				break
			}
		}
	}

	if !hasContent && !hasOtherKeys && state.SyncAgentsCreated {
		// We created this file and it's now empty — remove.
		if err := os.Remove(claudeSettingsPath); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		a.Info("hooks: removed empty settings.json (no hooks, no user content)")
	} else if hasContent || hasOtherKeys {
		// Merge hook entries into the raw JSON so we preserve any
		// non-hooks keys with their original ordering.
		if err := writeSettingsJSON(claudeSettingsPath, settings, rawSettings, state.SyncAgentsCreated || !fileExists(claudeSettingsPath)); err != nil {
			return 0, err
		}
	}

	// Write state file (even if empty — record that we cleaned up).
	if err := writeHooksState(statePath, newState); err != nil {
		return 0, err
	}

	return len(frags), nil
}

// readHookFragments reads and parses every .json file under hooksDir,
// sorted by name for deterministic output.
func (a *App) readHookFragments(hooksDir string) ([]HookFragment, error) {
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var frags []HookFragment
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()[:len(e.Name())-5] // strip .json
		path := filepath.Join(hooksDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			a.Warn(fmt.Sprintf("hooks/%s: read error: %v", e.Name(), err))
			continue
		}
		var f HookFragment
		if err := json.Unmarshal(data, &f); err != nil {
			a.Warn(fmt.Sprintf("hooks/%s: invalid JSON: %v", e.Name(), err))
			continue
		}
		if f.Event == "" {
			a.Warn(fmt.Sprintf("hooks/%s: missing required \"event\" key — skipped", e.Name()))
			continue
		}
		f.Name = name
		frags = append(frags, f)
	}
	sort.Slice(frags, func(i, j int) bool { return frags[i].Name < frags[j].Name })
	return frags, nil
}

// readSettingsJSON reads .claude/settings.json, returning:
//   - parsed hooks subset (always non-nil)
//   - the full raw JSON as a map (preserved for non-hooks keys)
//   - error only on I/O failures, not parse failures (missing/malformed → empty)
func readSettingsJSON(path string) (*claudeSettings, map[string]json.RawMessage, error) {
	empty := &claudeSettings{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil, nil
		}
		return nil, nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Unparseable — treat as empty (don't clobber user's file).
		return empty, nil, nil
	}
	var s claudeSettings
	if hooksRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &s.Hooks); err != nil {
			// hooks key exists but isn't the expected shape.
			// Don't touch it.
			return empty, raw, nil
		}
	}
	return &s, raw, nil
}

// readHooksState reads the ownership sidecar.
func readHooksState(path string) (*hooksStateInternal, error) {
	s := &hooksStateInternal{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return s, nil // corrupted → start fresh
	}
	if s.Entries == nil {
		s.Entries = make(map[string]hooksStateEntry)
	}
	return s, nil
}

// hooksStateInternal is the on-disk shape, extended with a creation
// flag that the public hooksState doesn't expose.
type hooksStateInternal struct {
	Entries           map[string]hooksStateEntry `json:"entries"`
	SyncAgentsCreated bool                       `json:"syncAgentsCreated,omitempty"`
}

// findHookOwner returns the fragment name that owns a given hook
// entry, or "" if no state entry matches (i.e. the entry is
// user-authored, not sync-agents-owned).
func (a *App) findHookOwner(event string, entry hookEntry, state *hooksStateInternal) string {
	for name, se := range state.Entries {
		if se.Event != event {
			continue
		}
		sha := sha256.Sum256([]byte(fmt.Sprintf("%v", entry.Hooks)))
		if fmt.Sprintf("%x", sha) == se.SHA {
			return name
		}
	}
	return ""
}

func hookEntriesEqual(a, b hookEntry) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// fileExists reports whether the path exists and is readable.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirExists reports whether the path exists and is a directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// writeSettingsJSON writes settings.json by merging hook entries into
// the raw JSON map (preserving non-hooks keys and their ordering),
// then writing atomically.
func writeSettingsJSON(path string, settings *claudeSettings, raw map[string]json.RawMessage, created bool) error {
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}
	if settings.Hooks != nil && len(settings.Hooks) > 0 {
		hj, err := json.Marshal(settings.Hooks)
		if err != nil {
			return err
		}
		raw["hooks"] = hj
	} else {
		delete(raw, "hooks")
	}
	out, err := jsonMarshalSorted(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'), 0644)
}

// writeHooksState writes the ownership sidecar.
func writeHooksState(path string, state hooksState) error {
	internal := hooksStateInternal{
		Entries:           state.Entries,
		SyncAgentsCreated: true, // we touched it
	}
	if internal.Entries == nil {
		internal.Entries = make(map[string]hooksStateEntry)
	}
	data, err := json.MarshalIndent(internal, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0644)
}

// jsonMarshalSorted marshals a map[string]json.RawMessage with
// sorted keys (Go's default map iteration is random), producing
// deterministic output. indent="" omits indentation; otherwise
// prefix + indent apply (like json.MarshalIndent).
func jsonMarshalSorted(m map[string]json.RawMessage, prefix, indent string) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf []byte
	buf = append(buf, '{')
	if indent != "" {
		buf = append(buf, '\n')
	}
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
			if indent != "" {
				buf = append(buf, '\n')
			}
		}
		if indent != "" {
			buf = append(buf, []byte(indent)...)
		}
		kbytes, _ := json.Marshal(k)
		buf = append(buf, kbytes...)
		buf = append(buf, ':')
		if indent != "" {
			buf = append(buf, ' ')
		}
		buf = append(buf, m[k]...)
	}
	if indent != "" && len(keys) > 0 {
		buf = append(buf, '\n')
	}
	buf = append(buf, '}')
	return buf, nil
}

// CleanHooks removes all sync-agents-owned entries from
// .claude/settings.json during global clean, then removes the
// state file. If settings.json becomes empty and sync-agents
// created it, the file is deleted (so .claude/ can be pruned).
// User-authored hooks and non-hooks keys pass through untouched.
func (a *App) CleanHooks(claudeSettingsPath, statePath string) (int, error) {
	state, err := readHooksState(statePath)
	if err != nil {
		return 0, err
	}
	if len(state.Entries) == 0 && !state.SyncAgentsCreated {
		return 0, nil
	}

	settings, raw, err := readSettingsJSON(claudeSettingsPath)
	if err != nil {
		return 0, err
	}

	removed := 0
	if settings.Hooks != nil {
		for event := range settings.Hooks {
			filtered := make([]hookEntry, 0, len(settings.Hooks[event]))
			for _, entry := range settings.Hooks[event] {
				owner := a.findHookOwner(event, entry, state)
				if owner != "" {
					removed++
					continue
				}
				filtered = append(filtered, entry)
			}
			if len(filtered) == 0 {
				delete(settings.Hooks, event)
			} else {
				settings.Hooks[event] = filtered
			}
		}
		if len(settings.Hooks) == 0 {
			settings.Hooks = nil
		}
	}

	hasOtherKeys := false
	if raw != nil {
		for k := range raw {
			if k != "hooks" {
				hasOtherKeys = true
				break
			}
		}
	}

	if settings.Hooks == nil && !hasOtherKeys && state.SyncAgentsCreated {
		if err := os.Remove(claudeSettingsPath); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	} else if settings.Hooks != nil || hasOtherKeys {
		if err := writeSettingsJSON(claudeSettingsPath, settings, raw, false); err != nil {
			return 0, err
		}
	}

	// Remove state file — clean means no more sync-agents footprint.
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return removed, nil
}
