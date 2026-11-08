package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// DestinationStrategy is the kind of filesystem operation `global
// sync` performs for a given (tool, artifact, semantic) tuple.
//
// Most tool/artifact pairs resolve to StrategySymlink — a per-artifact
// symlink at a per-tool path. A few resolve to StrategyConcat
// (Windsurf passive rules, Copilot, Codex), where multiple artifacts
// merge into one tool-owned file. StrategySkip is for cases that
// can't be cleanly represented in a tool's filesystem layout (today:
// multi-file invocable skills targeting Windsurf workflows).
type DestinationStrategy int

const (
	// StrategySymlink: create an absolute symlink at the resolved path
	// pointing at the canonical artifact under ~/.agents/.
	StrategySymlink DestinationStrategy = iota

	// StrategyConcat: the artifact contributes to a tool's merged
	// file (e.g. ~/.codeium/windsurf/memories/global_rules.md). The
	// concat layer regenerates that file once per sync, not once per
	// artifact. See concat.go for the regeneration logic.
	StrategyConcat

	// StrategySkip: the artifact cannot be routed to this tool.
	// SkipReason explains why (multi-file invocable skill targeting
	// Windsurf, etc.). The sync loop logs the reason and continues
	// to other tools.
	StrategySkip
)

// Destination is the resolved location for one (tool, artifact,
// semantic) tuple. Strategy determines how the orchestration layer
// handles it; Path is the absolute target path (for symlink) or the
// concat file (for concat).
type Destination struct {
	// Strategy is how to write this destination — see
	// DestinationStrategy.
	Strategy DestinationStrategy

	// Path is the absolute filesystem path. Its meaning depends on
	// Strategy:
	//   - StrategySymlink: the symlink to create (or repair).
	//   - StrategyConcat: the concat output file that this artifact
	//     contributes to. The orchestration layer collects all
	//     artifacts pointing at the same concat Path and regenerates
	//     it as a single file.
	//   - StrategySkip: empty.
	Path string

	// Target is what the symlink at Path points at. Set only when
	// Strategy == StrategySymlink: the artifact file for rules,
	// workflows and other single-file artifacts; for skills, the skill
	// directory (Claude) or the SKILL.md inside it (other tools).
	Target string

	// SkipReason is set only when Strategy == StrategySkip. Used by
	// the orchestration layer to print a per-skip warning.
	SkipReason string
}

// TargetDestination resolves one (tool, artifact) pair to its
// Destination, given the artifact's already-resolved Semantic and
// the location of the canonical artifact under ~/.agents/.
//
// Parameters:
//
//   - tool: the destination tool (from the Tools registry).
//   - typ: artifact bucket type (rule / skill / workflow).
//   - name: artifact name (without extension or directory).
//   - sem: resolved semantic — caller has already inspected frontmatter
//     and bucket defaults via ResolveSemantic.
//   - artifactSourcePath: absolute path to the artifact under
//     ~/.agents/ (the file for rules/workflows; the directory for
//     skills). Used to detect multi-file skills, which Windsurf can't
//     accept as workflows.
//   - globalRootParent: parent dir of the resolved global root (used
//     to compute per-tool dirs like ~/.claude/).
//
// Returns a Destination that the orchestration layer can act on. This
// function is pure — no filesystem writes — so it's safe for
// dry-run plans and for tests that don't want to populate every
// per-tool dir.
//
// See SPEC-002 §Semantic-aware routing and
// docs/architecture/semantic-routing.md for the routing rules this
// implements.
func TargetDestination(
	tool Tool,
	typ ArtifactType,
	name string,
	sem Semantic,
	artifactSourcePath string,
	globalRootParent string,
) Destination {
	dest := routeDestination(tool, typ, name, sem, artifactSourcePath, globalRootParent)
	if dest.Strategy == StrategySymlink {
		dest.Target = symlinkTargetFor(tool.ID, typ, artifactSourcePath)
	}
	return dest
}

// symlinkTargetFor returns what a tool's per-artifact symlink points
// at.
//
// Claude links a skill's whole directory: Claude reads a skill from
// ~/.claude/skills/<name>/, so the skill's supporting files (scripts,
// templates, references) are only reachable when that path is the
// skill directory itself. The other symlink tools take a single
// markdown file per artifact, so they link the skill's SKILL.md.
// Rules, workflows and the other single-file artifacts link the file.
func symlinkTargetFor(toolID string, typ ArtifactType, artifactSourcePath string) string {
	if typ != ArtifactSkill {
		return artifactSourcePath
	}
	if toolID == "claude" {
		return artifactSourcePath
	}
	return filepath.Join(artifactSourcePath, "SKILL.md")
}

// routeDestination resolves the destination path and strategy for
// one (tool, artifact) pair; TargetDestination adds the symlink
// target.
func routeDestination(
	tool Tool,
	typ ArtifactType,
	name string,
	sem Semantic,
	artifactSourcePath string,
	globalRootParent string,
) Destination {
	// Agents (subagent definitions) route independently of semantic:
	// Claude is the only registered tool with a subagent surface
	// (~/.claude/agents/), so every other tool skips. Routing them
	// through the per-tool semantic tables would mislabel them as
	// commands (Claude), workflows (Windsurf), or concat fodder
	// (Copilot/Codex).
	if typ == ArtifactAgent {
		if tool.ID == "claude" {
			return Destination{
				Strategy: StrategySymlink,
				Path:     filepath.Join(globalRootParent, ".claude", "agents", name+".md"),
			}
		}
		return Destination{
			Strategy:   StrategySkip,
			SkipReason: fmt.Sprintf("%s has no subagent surface", tool.ID),
		}
	}

	// Reference docs (plans/specs, SPEC-004 Part D) also route
	// independently of semantic: symlinked under .claude/ so they're
	// @-mentionable, skipped everywhere else — never concatenated
	// into always-on instructions (that would preload reference
	// material into baseline context).
	if typ == ArtifactPlan || typ == ArtifactSpec {
		bucket, _ := BucketForArtifact(typ)
		if tool.ID == "claude" {
			return Destination{
				Strategy: StrategySymlink,
				Path:     filepath.Join(globalRootParent, ".claude", bucket.Dir, name+".md"),
			}
		}
		return Destination{
			Strategy:   StrategySkip,
			SkipReason: fmt.Sprintf("%s reference docs are Claude-only; other tools read them via the AGENTS.md index", bucket.Dir),
		}
	}

	// Hooks (SPEC-004 Part C) are never synced one-by-one — they
	// are collected and merged into .claude/settings.json in one
	// batch pass after the per-artifact loop. The per-artifact
	// routing always skips; the batch pass handles everything.
	if typ == ArtifactHook {
		return Destination{
			Strategy:   StrategySkip,
			SkipReason: "hooks are batch-merged into settings.json (see MergeHooks)",
		}
	}

	switch tool.ID {
	case "claude":
		return claudeDestination(typ, name, sem, globalRootParent)
	case "codeium":
		return codeiumDestination(typ, name, sem, artifactSourcePath, globalRootParent)
	case "cursor":
		return cursorDestination(typ, name, sem, globalRootParent)
	case "copilot":
		return copilotDestination(globalRootParent)
	case "codex":
		return codexDestination(globalRootParent)
	default:
		return Destination{
			Strategy:   StrategySkip,
			SkipReason: fmt.Sprintf("unknown tool %q", tool.ID),
		}
	}
}

// claudeDestination implements the Claude row of the routing table:
//
//   - Invocable skill (dir): ~/.claude/skills/<name> (a link to the skill directory)
//   - Invocable rule/workflow (single file): ~/.claude/commands/<name>.md
//   - Passive (any bucket): ~/.claude/rules/<name>.md (single file)
//
// The "passive multi-file" case (a skill explicitly marked
// invocable: false) is awkward — Claude's passive surface is a single
// rules/*.md file, not a directory. We currently treat it as
// StrategySkip with a warning, leaving the artifact untouched in this
// tool. A future revision could flatten the skill's SKILL.md into
// rules/. Tracking that as a non-blocking decision.
func claudeDestination(typ ArtifactType, name string, sem Semantic, parent string) Destination {
	claudeDir := filepath.Join(parent, ".claude")
	switch sem {
	case Invocable:
		if typ == ArtifactSkill {
			return Destination{
				Strategy: StrategySymlink,
				Path:     filepath.Join(claudeDir, "skills", name),
			}
		}
		// Single-file invocable (rule or workflow marked
		// invocable: true) — lands in Claude's commands/ surface.
		return Destination{
			Strategy: StrategySymlink,
			Path:     filepath.Join(claudeDir, "commands", name+".md"),
		}
	case Passive:
		if typ == ArtifactSkill {
			// Multi-file artifact (skill dir) explicitly marked
			// passive. No clean single-file destination — skip and
			// warn so the user can decide whether to split the
			// skill or accept the gap.
			return Destination{
				Strategy:   StrategySkip,
				SkipReason: fmt.Sprintf("passive skill %q has no single-file Claude destination", name),
			}
		}
		return Destination{
			Strategy: StrategySymlink,
			Path:     filepath.Join(claudeDir, "rules", name+".md"),
		}
	}
	return Destination{Strategy: StrategySkip, SkipReason: "unknown semantic"}
}

// codeiumDestination implements the Codeium/Windsurf row:
//
//   - Invocable single-file: ~/.codeium/windsurf/global_workflows/<name>.md
//   - Invocable multi-file skill: SKIP (Windsurf workflows are single
//     .md files; a skill dir with supporting files can't be a
//     workflow without flattening)
//   - Passive: concat into ~/.codeium/windsurf/memories/global_rules.md
//
// Multi-file detection: a skill is "multi-file" if its source dir
// contains anything besides SKILL.md. SkillIsMultiFile inspects the
// directory; if the inspection fails we fall back to treating the
// skill as single-file (safer to attempt the workflow placement and
// let the symlink layer either succeed or fail loudly).
func codeiumDestination(typ ArtifactType, name string, sem Semantic, artifactSourcePath, parent string) Destination {
	windsurfBase := filepath.Join(parent, ".codeium", "windsurf")

	switch sem {
	case Invocable:
		if typ == ArtifactSkill && SkillIsMultiFile(artifactSourcePath) {
			return Destination{
				Strategy:   StrategySkip,
				SkipReason: fmt.Sprintf("Windsurf workflows are single-file; skill %q has supporting files", name),
			}
		}
		// Single-file invocable: workflow file, single-file skill,
		// or rule marked invocable: true.
		invocableSource := name + ".md"
		if typ == ArtifactSkill {
			// A single-file skill is still a directory containing
			// only SKILL.md. The workflow file gets the skill name
			// as its identifier; the symlink points at SKILL.md
			// inside the source dir.
			invocableSource = name + ".md"
		}
		return Destination{
			Strategy: StrategySymlink,
			Path:     filepath.Join(windsurfBase, "global_workflows", invocableSource),
		}
	case Passive:
		return Destination{
			Strategy: StrategyConcat,
			Path:     filepath.Join(windsurfBase, "memories", "global_rules.md"),
		}
	}
	return Destination{Strategy: StrategySkip, SkipReason: "unknown semantic"}
}

// cursorDestination implements the Cursor row. Cursor doesn't
// distinguish invocable from passive at the filesystem level — both
// land in ~/.cursor/rules/<name>.md. For skills (dirs), we symlink
// the SKILL.md inside the dir as <name>.md.
func cursorDestination(typ ArtifactType, name string, sem Semantic, parent string) Destination {
	_ = sem // intentionally unused — Cursor has no per-semantic split
	return Destination{
		Strategy: StrategySymlink,
		Path:     filepath.Join(parent, ".cursor", "rules", name+".md"),
	}
}

// copilotDestination implements the Copilot row: every artifact
// merges into one instructions.md regardless of semantic. The
// orchestration layer collects all artifacts pointing at this Path
// and regenerates the file once.
func copilotDestination(parent string) Destination {
	return Destination{
		Strategy: StrategyConcat,
		Path:     filepath.Join(parent, ".github", "copilot", "instructions.md"),
	}
}

// codexDestination implements the Codex row: same shape as Copilot,
// different file location.
func codexDestination(parent string) Destination {
	return Destination{
		Strategy: StrategyConcat,
		Path:     filepath.Join(parent, ".codex", "instructions.md"),
	}
}

// SkillIsMultiFile reports whether the skill directory at the given
// path contains any files besides SKILL.md (or subdirectories of
// any kind).
//
// A skill is considered single-file if SKILL.md is the only entry
// inside its directory. Anything else — supporting Python scripts,
// example files, sub-directories — makes it multi-file.
//
// Returns false (single-file) when:
//
//   - The path doesn't exist or isn't a directory. We can't say
//     definitively, but treating it as single-file lets the symlink
//     layer either succeed (if SKILL.md is actually a single file at
//     that path) or fail with a clearer error than a fabricated
//     warning here would produce.
//   - The directory contains only SKILL.md.
//
// Returns true otherwise.
func SkillIsMultiFile(skillDir string) bool {
	fi, err := os.Stat(skillDir)
	if err != nil || !fi.IsDir() {
		return false
	}
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != "SKILL.md" {
			return true
		}
	}
	return false
}
