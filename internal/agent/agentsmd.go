package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
)

// Markers for the AGENTS.md mirror inside a global CLAUDE.md.
//
// A separate pair from ManagedImportBlockStart/End so the two managed
// regions can coexist and be regenerated independently. Content
// outside both pairs is preserved.
const (
	AgentsMDBlockStart = "<!-- sync-agents:agents-md:start -->"
	AgentsMDBlockEnd   = "<!-- sync-agents:agents-md:end -->"
)

const agentsMDBanner = "<!-- mirrored from AGENTS.md by sync-agents; edit AGENTS.preamble.md, not this block -->"

// ResolveGlobalAgentsMDPath returns the absolute path of the AGENTS.md
// that belongs to the global agents tree, or "" when it cannot be
// located.
//
// The global root is conventionally a symlink (~/.agents ->
// <repo>/.agents), and AGENTS.md lives in the repo that owns the tree —
// one level above the resolved .agents directory, NOT above the
// symlink. Resolving lexically would look for AGENTS.md next to the
// symlink (e.g. $HOME/AGENTS.md) and silently find nothing, so the
// symlink is resolved first.
func ResolveGlobalAgentsMDPath(globalRoot string) string {
	resolved, err := filepath.EvalSymlinks(globalRoot)
	if err != nil {
		resolved = globalRoot
	}
	candidate := filepath.Join(filepath.Dir(resolved), "AGENTS.md")
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

// agentsRelativeLinkRegexp matches markdown links whose target is
// relative to the agents repo root, e.g. `](.agents/rules/x.md)`.
var agentsRelativeLinkRegexp = regexp.MustCompile(`\]\(\.agents/`)

// AbsolutizeAgentsMDLinks rewrites repo-relative links in AGENTS.md so
// they still resolve once the content is mirrored into a CLAUDE.md that
// lives somewhere else.
//
// AGENTS.md indexes artifacts as `](.agents/rules/x.md)`, which is
// correct relative to the agents repo. Copied verbatim into
// ~/.claude/CLAUDE.md those become ~/.claude/.agents/rules/x.md, which
// does not exist — every index link would dangle. Rewriting to absolute
// paths keeps them working from any location.
func AbsolutizeAgentsMDLinks(content, agentsRepoRoot string) string {
	return agentsRelativeLinkRegexp.ReplaceAllString(
		content,
		"]("+agentsRepoRoot+string(filepath.Separator)+".agents/",
	)
}

// BuildAgentsMDBlock renders the managed AGENTS.md mirror block.
func BuildAgentsMDBlock(agentsMDContent, agentsRepoRoot string) string {
	var buf bytes.Buffer
	buf.WriteString(AgentsMDBlockStart)
	buf.WriteString("\n")
	buf.WriteString(agentsMDBanner)
	buf.WriteString("\n")
	buf.WriteString(AbsolutizeAgentsMDLinks(agentsMDContent, agentsRepoRoot))
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		buf.WriteString("\n")
	}
	buf.WriteString(AgentsMDBlockEnd)
	buf.WriteString("\n")
	return buf.String()
}

// RegenerateAgentsMDBlock mirrors the global AGENTS.md into claudeMDPath
// as a managed block, reporting whether the file changed.
//
// Returns (false, nil) when there is no AGENTS.md to mirror — the
// feature is opt-in and its absence is not an error, exactly as a
// missing preamble is not.
func RegenerateAgentsMDBlock(claudeMDPath, agentsMDPath string) (bool, error) {
	if agentsMDPath == "" {
		return false, nil
	}
	raw, err := os.ReadFile(agentsMDPath)
	if err != nil {
		return false, err
	}

	newBlock := BuildAgentsMDBlock(string(raw), filepath.Dir(agentsMDPath))

	existing, readErr := os.ReadFile(claudeMDPath)
	var out string
	if readErr != nil {
		out = newBlock
	} else {
		out = replaceBlockBetweenMarkers(string(existing), newBlock, AgentsMDBlockStart, AgentsMDBlockEnd)
		if out == string(existing) {
			return false, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(claudeMDPath), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(claudeMDPath, []byte(out), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
