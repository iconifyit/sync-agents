---
id: SPEC-002
title: "Promote to Global: sync skills, rules, and workflows to provider global dirs"
status: Draft
owner: nmccready
created: 2026-05-13
updated: 2026-05-14 (rev 4: Technical Design snippets re-synced to PR A + PR B implementation — Tool registry replaces string-switch ResolveGlobalTargetDir; method renamed GlobalRoot → ResolveGlobalRoot; CmdPromote takes ArtifactType not bare string; `--sync` flag explicitly tagged as PR-C deferred)
prior_revisions:
  - rev 3 (2026-05-14): added semantic-aware routing — buckets are not behavioral categories; invocable vs passive frontmatter contract; Claude skill ⇄ Windsurf workflow re-routing; passive concat for Windsurf rules
  - rev 2 (2026-05-14): merged SPEC-002-global-scope-promotion into this doc; copy semantics confirmed; env-var root override + path-form promote + idempotency requirements folded in; demote/relocate deferred to Future Work
  - rev 1 (2026-05-13): initial draft

implementation_status:
  - PR A landed (a84c13f): Scope + Tool types + ResolveGlobalRoot resolver. Foundation only — no behavior change to existing commands.
  - PR B landed (f63d143): CmdPromote (both invocation forms) + CmdGlobalInit + their CLI wiring + 21 unit tests + user-facing docs.
  - PR C pending: CmdGlobalSync with semantic-aware routing, concat regeneration, `--sync` flag on `promote`.
  - PR D pending: CmdGlobalStatus + CmdGlobalClean.
  - PR E pending: Makefile/npm test wiring for `go test ./...`, README updates, bats integration.
---

# [SPEC-002] Feature: Promote / Global Sync for Skills, Rules, and Workflows

## Overview

Add a `promote` command and a `global` subcommand group so that any item in a
project's `.agents/` directory can be elevated to a user-level global store
(`~/.agents/`), and then synced from there to the standard global config
directories of every supported AI coding agent.

This spec also establishes the **Go test suite** (`go test ./...`) as a
first-class requirement alongside the existing bats integration tests.

## Motivation

Today `sync-agents sync` links `.agents/` into per-project provider dirs
(`.claude/`, `.windsurf/`, `.cursor/`, `.github/copilot/`). There is no path
to make a rule, skill, or workflow available _globally_ — across every project
on the machine — without manually copying files.

Provider conventions for global config differ from local:

| Provider | Local dir          | Global dir (user-level)         |
| -------- | ------------------ | ------------------------------- |
| Claude   | `.claude/`         | `~/.claude/`                    |
| Windsurf | `.windsurf/`       | (local only; Codeium is global) |
| Codeium  | `.windsurf/`       | `~/.codeium/`                   |
| Cursor   | `.cursor/`         | `~/.cursor/`                    |
| Copilot  | `.github/copilot/` | `~/.github/copilot/`            |
| Codex    | `.codex/`          | `~/.codex/`                     |

The `.windsurf/` directory is **local-only**; Codeium's global directory
(`~/.codeium/`) is the correct global target for Windsurf/Codeium users.

## Goals

- `sync-agents promote <type> <name>` copies a local `.agents/` item into the
  global store (`~/.agents/`) and optionally triggers a global sync.
- `sync-agents promote <path>` is sugar for the above: detect `<type>` and
  `<name>` from a conventional path (see "Auto-detect from path" below).
- `sync-agents global init` creates `~/.agents/` with the standard skeleton.
- `sync-agents global sync` links `~/.agents/` subdirectories into all
  supported global provider dirs.
- `sync-agents global status` reports the state of global symlinks.
- `sync-agents global clean` removes global symlinks created by sync-agents.
- A configurable global root (env var + flag) lets tests and shared-machine
  setups redirect `~/.agents/` to an alternate prefix.
- Go unit tests (`go test ./...`) cover all promote and global-sync logic;
  bats tests cover CLI surface and integration.

## Non-Goals

- **`demote` / `relocate` (inverse direction)** — moving a global artifact
  back into a project is deferred. Manual copy works today; a dedicated
  command can ship in a follow-up spec once promote stabilizes.
- **Merging or diffing global vs local versions** (tracked separately).
- **Three-way merge** of divergent local/global copies. Conflict policy is
  fail-fast with `--force` override; no automatic merge.
- **Network / remote global stores** (e.g., git-hosted shared `.agents/`).
  Users who want a versioned `~/.agents/` can use their own dotfiles tooling.
- **Cross-machine sync.**
- **Settings/hooks fragments** (`.claude/settings.json` JSON patches). These
  are not simple file copies and need their own design; tracked as a future
  spec.
- **Automatic conflict resolution** — local always wins on `promote --force`,
  global always wins on `global pull` (future spec).
- **Homebrew or system-level install paths.**
- **MCP server installs.**

## Definitions

| Term | Meaning |
|---|---|
| **Project scope** | The cwd's `.agents/` and per-tool dirs (`.claude/`, `.windsurf/`, `.cursor/`, `.github/copilot/`). |
| **Global scope** | `~/.agents/` and per-tool user dirs (`~/.claude/`, `~/.codeium/`, `~/.cursor/`, `~/.github/copilot/`, `~/.codex/`). Override-able via `--global-root` or `$SYNC_AGENTS_GLOBAL_ROOT`. |
| **Promote** | Copy a project artifact into the global store. Copy, not symlink (see "Copy semantics" below). |
| **Global sync** | Symlink global per-tool directories into `~/.agents/`. |
| **Artifact** | A rule, skill, or workflow. (Settings/hooks deferred.) |
| **Semantic** | The *behavioral category* of an artifact: `invocable` (loaded on demand by name or by the model's trigger decision) or `passive` (always loaded as context). Bucket name and semantic are NOT the same thing — see below. |

---

## Semantic categories (bucket ≠ semantic)

The three `.agents/` buckets (`rules/`, `skills/`, `workflows/`) are
**directory conventions**, not behavioral categories. Different AI tools
treat the same bucket name as a different *semantic*:

| Bucket | Claude semantic | Windsurf / Codeium semantic | Cursor semantic | Copilot / Codex semantic |
|---|---|---|---|---|
| `rules/` | passive (always-on) | passive (concat → `memories/global_rules.md`) | passive (`rules/`) | passive (flat `instructions.md`) |
| `skills/` | **invocable** (Skill tool decides) | **passive** (auto-loaded as memory; no auto-run) | passive | passive |
| `workflows/` | passive (reference doc) | **invocable** (slash-style flow) | passive | passive |

The two semantics are:

- **`invocable`** — the artifact is loaded only when chosen by name (user
  slash command, or the model's tool-call decision based on the artifact's
  trigger description). Not in baseline context.
- **`passive`** — the artifact is part of baseline context for every
  conversation. No trigger logic; always loaded.

### Frontmatter contract

Each artifact MAY declare its semantic in YAML frontmatter:

```yaml
---
invocable: true   # or false
---
```

If `invocable` is absent, the default is **inferred from the bucket**:

| Bucket | Default `invocable` |
|---|---|
| `rules/` | `false` (passive) |
| `skills/` | `true` (invocable) |
| `workflows/` | `true` (invocable) |

Existing artifacts that don't carry frontmatter MUST continue to work — the
bucket-based default covers them. Authors who want non-default behavior
(e.g., a passive skill that's really a long-form rule, or an invocable
workflow that's really a one-pager reference) opt in by writing the
frontmatter.

### Routing rule (the point of this section)

When `global sync` writes a `~/.agents/<bucket>/<name>` artifact to a target
tool's directory, the destination subdir SHALL be chosen by **semantic**,
not by bucket. Each tool declares two destination patterns — one for
`invocable`, one for `passive` — and the artifact's resolved `invocable`
value picks which one is used.

Per-tool destination map:

| Tool | Invocable destination | Passive destination |
|---|---|---|
| Claude | `~/.claude/skills/<name>`, a link to the skill directory (for skill dirs), or `~/.claude/commands/<name>.md` (for single-file invocables) | `~/.claude/rules/<name>.md` |
| Windsurf / Codeium | `~/.codeium/windsurf/global_workflows/<name>.md` (single-file) — directory invocables get flattened or rejected (see scenario) | concat into `~/.codeium/windsurf/memories/global_rules.md` |
| Cursor | `~/.cursor/rules/<name>.md` (Cursor has no separate invocable surface today — write passive) | `~/.cursor/rules/<name>.md` |
| Copilot | `~/.github/copilot/instructions.md` (concat) | `~/.github/copilot/instructions.md` (concat) |
| Codex | `~/.codex/instructions.md` (concat) | `~/.codex/instructions.md` (concat) |

Notes on degenerate cases:

- **Cursor / Copilot / Codex don't distinguish.** Both semantics land in the
  same place. The mapping is still computed via semantic for consistency;
  it just happens to resolve to one path. No fan-out, no duplication.
- **Windsurf invocables must be single-file.** Windsurf workflows are
  single `.md` files, not directories. A multi-file Claude skill cannot
  cleanly become a Windsurf workflow — see the corresponding scenario.
- **Concat targets** (`memories/global_rules.md`, Copilot/Codex
  `instructions.md`) merge all passive artifacts into one file, in
  alphabetical order of `<name>`, with `## <name>` headings and a
  "do-not-edit, generated by sync-agents" banner at the top.

---

## Requirements

### Requirement: Global agents directory

The system SHALL treat `~/.agents/` as the global source of truth, mirroring
the same directory structure as a project-level `.agents/`.

#### Scenario: Init creates global skeleton

- **GIVEN** `~/.agents/` does not exist
- **WHEN** `sync-agents global init` runs
- **THEN** `~/.agents/rules/`, `~/.agents/skills/`, `~/.agents/workflows/`
  are created
- **AND** `~/.agents/config` is written with the full global target list
  (`claude,codeium,cursor,copilot,codex`)
- **AND** `sync-agents global status` shows all targets as `[not synced]`

#### Scenario: Init is idempotent

- **GIVEN** `~/.agents/` already exists with content
- **WHEN** `sync-agents global init` runs again
- **THEN** existing files are preserved
- **AND** missing subdirs are created
- **AND** the command exits 0

---

### Requirement: Promote command

The system SHALL provide a `promote` command that copies a named item from the
project's `.agents/` directory into `~/.agents/`, making it globally available.

Supported types: `rule`, `skill`, `workflow`.

Invocation forms:

1. **Type + name (canonical):** `sync-agents promote <type> <name>` —
   e.g. `sync-agents promote skill my-skill`.
2. **Path (sugar):** `sync-agents promote <path>` — detect `<type>` and
   `<name>` from a conventional path. The detection rules are:
   - Path ends in `SKILL.md` OR is a directory containing `SKILL.md` →
     **skill**; name is the directory basename.
   - Path is under `.agents/rules/` and ends in `.md` → **rule**; name is
     the basename minus `.md`.
   - Path is under `.agents/workflows/` and ends in `.md` → **workflow**;
     name is the basename minus `.md`.
   - Anything else → error with a suggestion to pass `<type> <name>`
     explicitly.

#### Scenario: Promote a rule

- **GIVEN** `.agents/rules/my-rule.md` exists in the current project
- **WHEN** `sync-agents promote rule my-rule` runs
- **THEN** `~/.agents/rules/my-rule.md` is created (copy, not symlink)
- **AND** `[info] Promoted rule my-rule to ~/.agents/rules/my-rule.md` is
  printed

#### Scenario: Promote a skill (directory layout)

- **GIVEN** `.agents/skills/my-skill/SKILL.md` exists
- **WHEN** `sync-agents promote skill my-skill` runs
- **THEN** `~/.agents/skills/my-skill/SKILL.md` is created (full dir copy)
- **AND** any supporting files within `my-skill/` are also copied

#### Scenario: Promote a workflow

- **GIVEN** `.agents/workflows/my-workflow.md` exists
- **WHEN** `sync-agents promote workflow my-workflow` runs
- **THEN** `~/.agents/workflows/my-workflow.md` is created

#### Scenario: Promote via path (sugar form)

- **GIVEN** `.agents/skills/cool-skill/SKILL.md` exists
- **WHEN** `sync-agents promote .agents/skills/cool-skill` runs
- **THEN** the artifact is detected as `skill cool-skill` and copied to
  `~/.agents/skills/cool-skill/`
- **AND** the outcome is identical to running
  `sync-agents promote skill cool-skill`

#### Scenario: Promote via path rejects unrecognised location

- **GIVEN** `notes/random.md` exists outside any conventional `.agents/`
  subdirectory
- **WHEN** `sync-agents promote notes/random.md` runs
- **THEN** the command exits non-zero
- **AND** stderr names the unrecognised path and suggests
  `promote <type> <name>` explicitly

#### Scenario: Conflict requires --force

- **GIVEN** `~/.agents/rules/my-rule.md` already exists
- **WHEN** `sync-agents promote rule my-rule` runs **without** `--force`
- **THEN** the command prints a warning and exits non-zero
- **AND** the existing file is unchanged

- **WHEN** `sync-agents promote rule my-rule --force` runs
- **THEN** the existing file is overwritten with the project version

#### Scenario: --dry-run shows intent without writing

- **GIVEN** `.agents/skills/cool-skill/SKILL.md` exists
- **WHEN** `sync-agents promote skill cool-skill --dry-run` runs
- **THEN** the command prints what would be copied
- **AND** no files are written to `~/.agents/`

#### Scenario: Promote with auto global-sync

- **GIVEN** a valid `~/.agents/` and global provider dirs are set up
- **WHEN** `sync-agents promote rule my-rule --sync` runs
- **THEN** the rule is promoted AND `sync-agents global sync` is invoked
  automatically

#### Scenario: Missing source is an error

- **GIVEN** `.agents/rules/nonexistent.md` does not exist
- **WHEN** `sync-agents promote rule nonexistent` runs
- **THEN** `[error] rule "nonexistent" not found in .agents/rules/` is printed
- **AND** the command exits non-zero

---

### Requirement: Global sync

The system SHALL provide `sync-agents global sync` to link `~/.agents/`
subdirectories into each supported global provider directory.

Global provider directory map:

| scope     | Target token | Global directory                                  | local dir mapping (for sync) / symlink | INVOCABLE ? (/)    | Context to Sync to                                          |
| --------- | ------------ | ------------------------------------------------- | -------------------------------------- | ------------------ | ----------------------------------------------------------- |
| skills    | `claude`     | `~/.claude/skills/${name}` (link to the skill dir) | `~/.claude/skills/${name}`             | YES                | 1 to 1                                                      |
| skills    | `windsurf`   | `~/.codeium/windsurf/skills/${name}/SKILL.md`     | `~/.windsurf/skills/${name}/SKILL.md`  | NO auto run        | 1 to 2 (workflows/skills) claudee skills picked by windsurf |
| skills    | `github`     | `~/.github/skills/${name}/SKILL.md`               | `~/.github/skills/${name}/SKILL.md`    | YES                | 1 to 1                                                      |
| rules     | `claude`     | `~/.claude/rules/${name}.md`                      | `~/.claude/rules/${name}.md`           | NO                 | 1 to 1                                                      |
| rules     | `windsurf`   | `~/.codeium/windsurf/memories/global_rules.md`    | IGNORED                                | NO                 | 1 to 0 or global concat                                     |
| workflows | `windsurf`   | `~/.codeium/windsurf/global_workflows/${name}.md` | `~/.windsurf/workflows/${name}.md`     | Yes ~ Claude Skill | 1 to 1                                                      |

Note: `.windsurf/` is **local-only**. The global Windsurf target is `codeium`
(`~/.codeium/`).

#### Scenario: Global sync links all targets

- **GIVEN** `~/.agents/` exists with `rules/`, `skills/`, `workflows/`
- **WHEN** `sync-agents global sync` runs
- **THEN** for each configured target, e.g. `claude`, the path
  `~/.claude/rules` is a symlink pointing to `~/.agents/rules`
- **AND** the same applies for `skills/` and `workflows/` in each target dir
- **AND** only subdirs that exist in `~/.agents/` are linked (no empty dir creation)

#### Scenario: --targets filters global sync

- **GIVEN** `~/.agents/` is initialized
- **WHEN** `sync-agents global sync --targets claude,codeium` runs
- **THEN** only `~/.claude/` and `~/.codeium/` are touched
- **AND** cursor, copilot, and codex dirs are not modified

#### Scenario: --dry-run for global sync

- **GIVEN** `~/.agents/` is initialized
- **WHEN** `sync-agents global sync --dry-run` runs
- **THEN** each proposed symlink is printed (`would link: …`)
- **AND** no filesystem changes are made

#### Scenario: Existing non-symlink conflicts

- **GIVEN** `~/.claude/rules` exists as a real directory (not a symlink)
- **WHEN** `sync-agents global sync` runs without `--force`
- **THEN** a warning is printed and that entry is skipped
- **WHEN** `sync-agents global sync --force` runs
- **THEN** the existing directory is removed and replaced with the symlink

---

### Requirement: Semantic-aware routing

The system SHALL route each artifact to a target tool's directory by its
**resolved semantic** (`invocable` | `passive`), not by its source bucket.
See "Semantic categories" above for the contract and per-tool destination map.

Semantic resolution order:

1. Artifact YAML frontmatter `invocable: true|false`.
2. Bucket default: `rules/` → passive, `skills/` → invocable,
   `workflows/` → invocable.

`global sync` SHALL use the resolved semantic for every destination
calculation. Symlink vs concat is implied by the destination:
- 1:1 paths (e.g. `~/.claude/skills/<name>`) get a symlink. A Claude skill links the skill directory, so its supporting files are reachable alongside `SKILL.md`.
- Concat paths (e.g. `~/.codeium/windsurf/memories/global_rules.md`) get a
  regenerated merged file each sync. Source `.agents/` files stay
  authoritative; the concat output is a derived artifact.

#### Scenario: Claude skill (invocable) lands in Windsurf global_workflows

- **GIVEN** `~/.agents/skills/cool-skill/SKILL.md` exists
- **AND** the file has no `invocable:` frontmatter (defaults to `true` per
  bucket)
- **AND** the skill is a single-file skill (only `SKILL.md`, no siblings)
- **WHEN** `sync-agents global sync --targets codeium` runs
- **THEN** `~/.codeium/windsurf/global_workflows/cool-skill.md` is a
  symlink (or copy on filesystems without symlink support) pointing at
  `~/.agents/skills/cool-skill/SKILL.md`
- **AND** `~/.codeium/windsurf/skills/cool-skill/` is **not** created
- **AND** `~/.codeium/windsurf/memories/global_rules.md` does **not**
  include this skill

#### Scenario: Claude rule (passive) concats into Windsurf memories

- **GIVEN** `~/.agents/rules/security.md` and `~/.agents/rules/style.md`
  exist
- **AND** neither file declares `invocable:` frontmatter (default passive)
- **WHEN** `sync-agents global sync --targets codeium` runs
- **THEN** `~/.codeium/windsurf/memories/global_rules.md` exists as a
  regular file
- **AND** its contents start with a generated banner line
- **AND** it includes `## security` and `## style` headings followed by the
  respective file bodies, alphabetized by name
- **AND** neither rule appears under `~/.codeium/windsurf/global_workflows/`

#### Scenario: Frontmatter override flips routing

- **GIVEN** `~/.agents/rules/onboarding.md` exists with frontmatter
  `invocable: true`
- **WHEN** `sync-agents global sync --targets codeium` runs
- **THEN** `~/.codeium/windsurf/global_workflows/onboarding.md` is the
  link/copy
- **AND** `onboarding` is **not** in the concat'd `global_rules.md`
- **AND** for Claude target, `~/.claude/commands/onboarding.md` is the
  destination (not `~/.claude/rules/`)

#### Scenario: Multi-file invocable skill cannot land in Windsurf workflow

- **GIVEN** `~/.agents/skills/big-skill/` contains `SKILL.md` plus other
  supporting files
- **AND** the skill is invocable (default)
- **WHEN** `sync-agents global sync --targets codeium` runs
- **THEN** the command prints a warning naming the skill and the reason
  ("Windsurf workflows are single-file; skill `big-skill` has supporting
  files")
- **AND** the skill is skipped for the codeium target (other targets still
  process it normally)
- **AND** the overall exit code remains 0 (warning, not error)

#### Scenario: Cursor degenerate case — both semantics same dir

- **GIVEN** `~/.agents/rules/security.md` (passive) and
  `~/.agents/skills/grep-helper/SKILL.md` (invocable) exist
- **WHEN** `sync-agents global sync --targets cursor` runs
- **THEN** both end up under `~/.cursor/rules/` (Cursor has no separate
  invocable surface)
- **AND** no error or warning is printed — this is the documented
  degenerate mapping

---

### Requirement: Global status

The system SHALL provide `sync-agents global status` to report the state of all
global provider directories and their symlinks.

#### Scenario: Status shows synced and missing targets

- **GIVEN** `~/.claude/rules` → `~/.agents/rules` (symlink exists)
- **AND** `~/.cursor/rules` does not exist
- **WHEN** `sync-agents global status` runs
- **THEN** output includes `[synced]  ~/.claude/rules -> ~/.agents/rules`
- **AND** output includes `[missing] ~/.cursor/rules`

---

### Requirement: Global clean

The system SHALL provide `sync-agents global clean` to remove symlinks created
by `global sync`.

#### Scenario: Clean removes symlinks

- **GIVEN** `~/.claude/rules` is a symlink to `~/.agents/rules`
- **WHEN** `sync-agents global clean` runs
- **THEN** `~/.claude/rules` is removed
- **AND** `~/.agents/rules` (the source) is untouched
- **AND** empty parent dirs created by sync-agents are removed

---

### Requirement: Idempotency and repair

Re-running `promote`, `global sync`, `global clean`, or `global init` on an
already-applied state SHALL be a no-op: exit 0, with no filesystem writes
beyond fixing broken or drifted symlinks if detected.

#### Scenario: Re-running global sync is a no-op when state is current

- **GIVEN** `sync-agents global sync` has already run successfully and all
  expected symlinks resolve correctly
- **WHEN** `sync-agents global sync` runs again
- **THEN** the command exits 0
- **AND** no filesystem writes occur (verified via stat mtimes)
- **AND** stdout summarizes "N targets already in sync" rather than echoing
  the full link list

#### Scenario: Repair recreates a broken symlink

- **GIVEN** `~/.claude/rules` is a symlink whose target no longer resolves
  (user deleted `~/.agents/rules/` manually or it moved)
- **WHEN** `sync-agents global sync` runs
- **THEN** the broken symlink is replaced with a fresh one pointing at the
  current `~/.agents/rules`
- **AND** stdout calls out the repair (`[repair] relinked ~/.claude/rules`)

---

### Requirement: Configurable global root

The default global root is `$HOME/.agents/`. The system SHALL allow this to
be overridden via, in order of precedence:

1. `--global-root <path>` flag, on any `global *` or `promote` command.
2. `$SYNC_AGENTS_GLOBAL_ROOT` environment variable.
3. The Go-level `App.GlobalRoot` field (used by tests).
4. Default: `$HOME/.agents/`.

This requirement exists so the test suite can use a `t.TempDir()` root, and
so corporate/shared machines can park the global tree somewhere other than
`$HOME`.

When `--global-root` is set, the per-tool global directories (`~/.claude/`,
`~/.codeium/`, …) are **also** rerouted under that root. Concretely, with
`--global-root=/tmp/x`, `~/.claude/rules` becomes `/tmp/x/.claude/rules`.
This keeps the override fully self-contained for tests.

#### Scenario: Env var redirects global root

- **GIVEN** `$SYNC_AGENTS_GLOBAL_ROOT=/tmp/fake-home` is set
- **AND** `/tmp/fake-home/` is empty
- **WHEN** `sync-agents global init` runs
- **THEN** `/tmp/fake-home/.agents/` is created
- **AND** `$HOME/.agents/` is **not** touched

#### Scenario: Flag overrides env var

- **GIVEN** `$SYNC_AGENTS_GLOBAL_ROOT=/tmp/a` is set
- **WHEN** `sync-agents global sync --global-root=/tmp/b` runs
- **THEN** `/tmp/b/.agents/` is the resolved root
- **AND** `/tmp/a/.agents/` is not touched

---

### Requirement: Go test suite

The system SHALL have a Go unit test suite covering all business logic in
`internal/`. Tests MUST run with `go test ./...` from the repo root. The
existing `npm test` command MUST run both bats AND Go tests concurrently.

The Makefile `test-go` target SHALL run `go test ./...` (not bats).

#### Scenario: go test passes on clean tree

- **GIVEN** a fresh clone of the repo
- **WHEN** `go test ./...` runs
- **THEN** all tests pass and the command exits 0

#### Scenario: promote logic is unit tested

- **GIVEN** a temporary directory acting as a fake project root and `~/.agents/`
- **WHEN** `App.CmdPromote("rule", "my-rule")` is called in a test
- **THEN** the rule file is copied to the fake global dir
- **AND** no filesystem side-effects outside the temp dirs occur

#### Scenario: global sync logic is unit tested

- **GIVEN** a populated fake `~/.agents/`
- **WHEN** `App.CmdGlobalSync()` is called
- **THEN** the expected symlinks are created in the fake global provider dirs
- **AND** `App.CmdGlobalStatus()` reports all as `[synced]`

#### Scenario: npm test runs go tests

- **GIVEN** a clean repo with Go and Node installed
- **WHEN** `npm test` runs
- **THEN** `go test ./...` and `bats test/sync-agents.bats` both execute
- **AND** the overall exit code is 0 on a green tree

---

## Acceptance Criteria

- **AC-1**: `sync-agents promote rule foo` copies `.agents/rules/foo.md` to
  `~/.agents/rules/foo.md` and exits 0. Without `--force`, an existing
  destination causes a warning + non-zero exit.
- **AC-2**: `sync-agents promote skill bar` deep-copies
  `.agents/skills/bar/` (including all files) to `~/.agents/skills/bar/`.
- **AC-3**: `sync-agents global sync` creates symlinks from `~/.agents/`
  subdirs into `~/.claude/`, `~/.codeium/`, `~/.cursor/`, `~/.github/copilot/`,
  `~/.codex/` for each subdir that exists.
- **AC-4**: `sync-agents global status` prints `[synced]` / `[missing]` /
  `[not a symlink]` for each target×subdir combination.
- **AC-5**: `sync-agents global clean` removes only symlinks that point into
  `~/.agents/`; it does not remove real directories.
- **AC-6**: `go test ./...` passes from repo root. Test coverage ≥ 70% on
  `internal/agent` package.
- **AC-7**: `make test-go` runs `go test ./...`, not bats.
- **AC-8**: `npm test` runs bats AND `go test ./...` concurrently; both must
  pass for overall success.
- **AC-9**: `.windsurf/` is never created by `global sync`; `codeium` is the
  only global Windsurf target.
- **AC-10**: `sync-agents global sync` regenerates `~/.agents/AGENTS.md` after
  linking, so Claude reads a current global index at `~/.claude/`.
- **AC-11**: `sync-agents global init` always writes
  `targets = claude,codeium,cursor,copilot,codex` regardless of any
  project-level `.agents/config`.
- **AC-12**: Given `$SYNC_AGENTS_GLOBAL_ROOT` (or `--global-root`) set to a
  temp dir, every `global *` and `promote` operation reads/writes under that
  temp dir and never touches `$HOME`.
- **AC-13**: Re-running any `global *` or `promote` command in a steady state
  is a no-op (exit 0, no filesystem writes); a broken symlink encountered
  during re-run is repaired and logged.
- **AC-14**: `sync-agents promote <path>` accepts a path under
  `.agents/{rules,skills,workflows}/…` and behaves identically to the
  `<type> <name>` form; unrecognised paths exit non-zero with guidance.
- **AC-15**: An invocable artifact (default for `skills/` and `workflows/`,
  or explicit frontmatter `invocable: true`) routes to each tool's
  invocable destination per "Semantic categories"; specifically a Claude
  skill bucket lands at `~/.codeium/windsurf/global_workflows/<name>.md`
  for the codeium target, not at `~/.codeium/windsurf/skills/`.
- **AC-16**: A passive artifact (default for `rules/`, or explicit
  frontmatter `invocable: false`) routes to each tool's passive
  destination; for codeium it is merged into
  `~/.codeium/windsurf/memories/global_rules.md` with alphabetized
  `## <name>` sections and a generated-by banner.
- **AC-17**: Artifact frontmatter `invocable: true|false` overrides the
  bucket default; the override applies uniformly across every target.
- **AC-18**: A multi-file invocable skill targeting codeium emits a warning
  and is skipped for codeium only; other targets still process it; exit
  code stays 0.

---

## Technical Design

### New `App` methods

```go
// internal/agent/agent.go (additions)

// CmdPromote promotes a single item from .agents/ to ~/.agents/.
// The path-form invocation is exposed as a thin shim in main.go that
// calls DetectArtifact() to derive (typ, name) before invoking this
// method. The library-level signature stays typed.
func (a *App) CmdPromote(typ ArtifactType, name string, opts PromoteOpts) error

// CmdGlobalInit initializes ~/.agents/ with the standard skeleton.
func (a *App) CmdGlobalInit() error

// CmdGlobalSync links ~/.agents/ subdirs into global provider dirs.
// (To be added in PR C.)
func (a *App) CmdGlobalSync(opts GlobalSyncOpts) error

// CmdGlobalStatus reports the state of global provider symlinks.
// (To be added in PR D.)
func (a *App) CmdGlobalStatus() error

// CmdGlobalClean removes global symlinks created by sync-agents.
// (To be added in PR D.)
func (a *App) CmdGlobalClean(opts GlobalCleanOpts) error

// ResolveGlobalRoot returns the resolved global agents directory.
// Resolution order: App.GlobalRoot field > $SYNC_AGENTS_GLOBAL_ROOT > $HOME/.agents.
// Also exposed: ResolveGlobalRootParent (returns filepath.Dir of the above)
// and ResolveToolDir(tool, scope) for callers that want the per-tool
// dir at either scope.
func (a *App) ResolveGlobalRoot() string
func (a *App) ResolveGlobalRootParent() string
func (a *App) ResolveToolDir(tool Tool, scope Scope) string
```

### Tool registry (replaces the string-switch ResolveGlobalTargetDir
sketch from rev 1–3)

A typed `Tool` struct with a per-`Scope` directory map replaces the
original string-keyed switch statement. The same five tools are
supported; the registry encodes the windsurf/codeium asymmetry once,
and any future tool gets added with one slice entry instead of patches
to every call site.

```go
// internal/agent/tool.go (new file)

type Tool struct {
    ID         string                  // canonical, lowercase
    Aliases    []string                // e.g. "windsurf" → codeium
    DirByScope map[Scope]string        // {ScopeLocal: ".windsurf",
                                       //  ScopeGlobal: ".codeium"}
    LocalOnly  bool                    // future opt-out for tools w/o global
}

var Tools = []Tool{
    {ID: "claude",  DirByScope: map[Scope]string{ScopeLocal: ".claude", ScopeGlobal: ".claude"}},
    {ID: "codeium", Aliases: []string{"windsurf"},
                    DirByScope: map[Scope]string{ScopeLocal: ".windsurf", ScopeGlobal: ".codeium"}},
    {ID: "cursor",  DirByScope: map[Scope]string{ScopeLocal: ".cursor", ScopeGlobal: ".cursor"}},
    {ID: "copilot", DirByScope: map[Scope]string{ScopeLocal: ".github/copilot", ScopeGlobal: ".github/copilot"}},
    {ID: "codex",   DirByScope: map[Scope]string{ScopeLocal: ".codex", ScopeGlobal: ".codex"}},
}

func (t Tool) DirForScope(scope Scope, parentRoot string) string
func (t Tool) HasScope(scope Scope) bool
func (t Tool) Matches(name string) bool

func ResolveTool(name string) (Tool, bool)
func ToolIDs() []string
func ToolIDsForScope(scope Scope) []string  // replaces the GlobalTargets constant
```

`ToolIDsForScope(ScopeGlobal)` returns the same five names the rev-3
draft called `GlobalTargets`. There is no separate `GlobalTargets`
constant — the registry is the single source of truth, and
LocalOnly-tool opt-out works automatically.

Note: `windsurf` is **not** a separate `Tool` in the registry. It is
an alias on the `codeium` Tool. `ResolveTool("windsurf")` and
`ResolveTool("codeium")` return the same value. This matches SPEC
intent (`.windsurf/` is local-only; `~/.codeium/` is the global) and
keeps the registry declarative.

### App.GlobalRoot field

```go
type App struct {
    // ... existing fields ...

    // GlobalRoot, when non-empty, overrides $SYNC_AGENTS_GLOBAL_ROOT and
    // $HOME/.agents. Per-tool dirs (~/.claude/, ~/.codeium/, …) are
    // rerouted under filepath.Dir(GlobalRoot).
    GlobalRoot string
}
```

### Promote — copy semantics

`promote` uses recursive copy, **not** symlinks, because:

1. The global store should not have project-relative symlinks.
2. The promoted copy is the global canonical version; the project copy is the
   source of the promotion, not an ongoing reference.

Skill promotion copies the entire directory tree:

```
.agents/skills/foo/ → ~/.agents/skills/foo/   (deep copy)
```

### Global sync — symlink semantics

`global sync` creates **absolute symlinks** from global provider dirs into
`~/.agents/`:

```
~/.claude/rules    → ~/.agents/rules    (absolute symlink)
~/.claude/skills   → ~/.agents/skills
~/.claude/workflows→ ~/.agents/workflows
~/.codeium/rules   → ~/.agents/rules
...
```

Unlike project-level sync (which uses relative symlinks), global sync uses
absolute paths to ensure symlinks remain valid regardless of the working
directory. Tests that set `--global-root` get absolute paths into the test
tempdir.

### Semantic resolution and routing

```go
// internal/agent/semantic.go (new file)

type Semantic string
const (
    Invocable Semantic = "invocable"
    Passive   Semantic = "passive"
)

// ResolveSemantic inspects YAML frontmatter for `invocable:` and falls
// back to the bucket default.
func ResolveSemantic(artifactPath, bucket string) (Semantic, error)

// TargetDestination yields the per-tool path for an artifact, picking
// the invocable vs passive destination per the routing table.
// Returns ("", reason, nil) when the artifact cannot be routed to this
// tool (e.g. multi-file invocable skill → codeium).
func TargetDestination(tool, name string, sem Semantic, isDir bool, globalRoot string) (path string, skipReason string, err error)

// ConcatTarget describes a tool's "merge all passive artifacts into one
// file" destination (Windsurf memories, Copilot/Codex instructions).
// global sync regenerates these atomically per run.
type ConcatTarget struct {
    Path        string  // e.g. ~/.codeium/windsurf/memories/global_rules.md
    Banner      string  // generated-by banner prepended on each regen
    HeadingTmpl string  // e.g. "## %s\n\n"
}
func ConcatTargetFor(tool string, globalRoot string) (ConcatTarget, bool)

// RegenerateConcat collects every passive artifact whose semantic resolves
// to the tool's concat target, sorts by name, and writes the merged file
// atomically (tmp + rename) so partial writes never corrupt the output.
func (a *App) RegenerateConcat(tool string) error
```

`CmdGlobalSync` becomes a loop that, for each `(artifact, tool)` pair,
calls `ResolveSemantic` then `TargetDestination` (or routes to the
concat collector). Concat regeneration runs once per tool, after the
per-artifact pass.

### Path-form promote (auto-detect)

```go
// detectArtifact maps a path under .agents/ to (type, name).
// Returns an error when not under a known subdirectory so the CLI
// can suggest the explicit type+name form. Shipped form is exported
// (DetectArtifact) and returns the typed ArtifactType, not a bare
// string.
func DetectArtifact(rel string) (ArtifactType, string, error) {
    rel = filepath.Clean(rel)
    rel = strings.TrimPrefix(rel, "./")
    slashed := filepath.ToSlash(rel)  // normalize for forward-slash prefix checks
    switch {
    case strings.HasPrefix(slashed, ".agents/skills/"):
        after := strings.TrimPrefix(slashed, ".agents/skills/")
        // first path component is the skill name; SKILL.md inside a
        // skill dir also resolves correctly because the name comes
        // from the segment before /SKILL.md.
        return ArtifactSkill, strings.SplitN(after, "/", 2)[0], nil
    case strings.HasPrefix(slashed, ".agents/rules/") && strings.HasSuffix(slashed, ".md"):
        return ArtifactRule, strings.TrimSuffix(filepath.Base(slashed), ".md"), nil
    case strings.HasPrefix(slashed, ".agents/workflows/") && strings.HasSuffix(slashed, ".md"):
        return ArtifactWorkflow, strings.TrimSuffix(filepath.Base(slashed), ".md"), nil
    }
    return "", "", fmt.Errorf("path %q is not under .agents/{rules,skills,workflows}/", rel)
}
```

### CLI surface

New top-level commands added to `main.go` / cobra command tree:

```
sync-agents promote <type> <name>     [--force] [--dry-run] [--sync*] [--global-root P]
sync-agents promote <path>            [--force] [--dry-run] [--sync*] [--global-root P]
sync-agents global init               [--global-root P]
sync-agents global sync               [--targets t1,t2] [--force] [--dry-run] [--global-root P]
sync-agents global status             [--targets t1,t2] [--global-root P]
sync-agents global clean              [--targets t1,t2] [--dry-run] [--global-root P]
```

`global` is a cobra command group; `init`, `sync`, `status`, `clean` are
subcommands.

### Go test infrastructure

New files: `internal/agent/agent_test.go` and `global_test.go`.

Test helpers use `t.TempDir()` to provide isolated fake project roots and
global roots. Tests set `App.GlobalRoot` directly. The CLI layer is
exercised via bats with `SYNC_AGENTS_GLOBAL_ROOT` pointing at a per-test
tempdir.

Updated `Makefile`:

```make
test-go:
	go test ./...

test: build
	SYNC_AGENTS_BIN=bin/sync-agents npx bats test/sync-agents.bats
```

`package.json` script updates:

```json
"test": "npx concurrently --names \"sh,go\" -c \"cyan,magenta\" \"npm run test:sh\" \"npm run test:unit\"",
"test:sh": "SYNC_AGENTS_BIN=bin/sync-agents npx bats test/sync-agents.bats",
"test:unit": "go test ./..."
```

---

## Test Plan

### Go unit tests (`go test ./...`)

- [x] `TestCmdPromote_Rule` — copies rule from fake project root to fake global root. (PR B)
- [x] `TestCmdPromote_Skill` — deep-copies skill dir; supporting files included. (PR B)
- [x] `TestCmdPromote_Workflow` — copies workflow file. (PR B)
- [x] `TestDetectArtifact_Skill` — `.agents/skills/foo` and `.../SKILL.md` both detected as `skill foo`. (PR B)
- [x] `TestDetectArtifact_RuleAndWorkflow` — file-form paths detected. (PR B)
- [x] `TestDetectArtifact_Unrecognised` — paths outside the buckets return error. (PR B)
- [x] `TestCmdPromote_ConflictNoForce` — returns error when destination exists. (PR B)
- [x] `TestCmdPromote_ConflictForce` — overwrites when `--force`. (PR B)
- [x] `TestCmdPromote_DryRun` — no files written, `[dry-run]` printed. (PR B)
- [x] `TestCmdPromote_MissingSource` — non-zero exit + error message names the artifact. (PR B)
- [x] `TestCmdPromote_AutoCreatesParents` — works without prior `global init`. (PR B)
- [x] `TestCmdGlobalInit_CreatesSkeleton` — creates skeleton dirs + canonical config. (PR B)
- [x] `TestCmdGlobalInit_Idempotent` — second run preserves existing files. (PR B)
- [x] `TestCmdGlobalInit_MissingSubdirRecreated` — partial-state recovery. (PR B)
- [x] `TestCmdGlobalInit_DryRun` — no fs changes, `[dry-run]` printed. (PR B)
- [x] `TestNormalizeArtifactType` — singular/plural/case normalization. (PR B)
- [ ] `TestGlobalSync` — symlinks created for all configured targets; `~/.agents/AGENTS.md` is regenerated.
- [ ] `TestGlobalSyncTargetFilter` — only specified targets touched.
- [ ] `TestGlobalSyncConflictNoForce` — existing real dir skipped with warning.
- [ ] `TestGlobalSyncConflictForce` — existing real dir replaced.
- [ ] `TestGlobalSyncDryRun` — no filesystem changes.
- [ ] `TestGlobalSyncIdempotent` — second run is no-op (no mtime changes).
- [ ] `TestGlobalSyncRepair` — broken symlink is recreated and logged.
- [ ] `TestResolveSemantic_BucketDefaults` — `rules/` → passive,
      `skills/` → invocable, `workflows/` → invocable when no frontmatter.
- [ ] `TestResolveSemantic_FrontmatterOverride` — `invocable: false` in a
      file under `skills/` resolves to passive.
- [ ] `TestRouteClaudeSkill_ToWindsurfWorkflow` — single-file invocable
      skill lands at `~/.codeium/windsurf/global_workflows/<name>.md`,
      not under `skills/`.
- [ ] `TestRoutePassiveRule_ToWindsurfMemoriesConcat` — two rules concat
      into `memories/global_rules.md` alphabetically with banner.
- [ ] `TestRouteMultiFileSkill_WindsurfSkipsWithWarning` — multi-file
      skill targeting codeium emits a warning and is skipped for codeium
      only; claude target still gets it; exit code 0.
- [ ] `TestRouteCursorDegenerate` — both invocable and passive land in
      `~/.cursor/rules/`; no warning.
- [ ] `TestRouteCopilotConcat` — Copilot/Codex concat into a single
      `instructions.md` regardless of semantic.
- [ ] `TestRegenerateConcat_Atomic` — concat target is written via
      tmp-file + rename; an injected mid-write failure leaves the
      original file intact.
- [ ] `TestGlobalStatus` — correct `[synced]` / `[missing]` output.
- [ ] `TestGlobalClean` — removes only sync-agents symlinks.
- [x] `TestTool_DirForScope_Codeium` / `TestResolveToolDir_*` — correct
      path for each tool at each scope; `windsurf` resolves to the same
      Tool as `codeium` via the alias mechanism. (PR A)
- [x] `TestResolveGlobalRoot_EnvBeatsHome` — `$SYNC_AGENTS_GLOBAL_ROOT` wins over `$HOME`. (PR A)
- [x] `TestResolveGlobalRoot_FieldPrecedence` — `App.GlobalRoot` wins over env. (PR A)
- [x] `TestResolveGlobalRoot_HomeDefault` — falls back to `$HOME/.agents`. (PR A)
- [x] `TestResolveGlobalRoot_AbsolutePathNormalization` — relative paths get expanded. (PR A)
- [x] `TestScope_String`, `TestParseScope_Valid`, `TestParseScope_Invalid` — Scope round-trip. (PR A)
- [x] `TestResolveTool_AliasResolution` — windsurf alias resolves to codeium. (PR A)
- [ ] `TestVersionSmoke` — `version.Version` is not empty string (regression
      guard from SPEC-001).

### bats integration tests (`test/sync-agents.bats`)

All bats tests SHALL run with `SYNC_AGENTS_GLOBAL_ROOT=$BATS_TMPDIR/global`
so they never touch the real `$HOME`.

- [ ] `promote rule` happy path via CLI.
- [ ] `promote skill` happy path via CLI.
- [ ] `promote <path>` sugar form.
- [ ] `global init` creates dirs.
- [ ] `global sync` creates symlinks; re-running is idempotent.
- [ ] `global status` output format.
- [ ] `global clean` removes symlinks.
- [ ] `promote --dry-run` prints intent, no writes.
- [ ] `global sync --targets claude` touches only Claude dir.
- [ ] `global sync` does **not** create `.windsurf/` anywhere under global root.
- [ ] Semantic routing end-to-end: a `.agents/skills/foo/SKILL.md` with no
      frontmatter lands at `~/.codeium/windsurf/global_workflows/foo.md`
      and `~/.claude/skills/foo` (a link to the skill dir) after a `promote skill foo &&
      global sync`. Verify `~/.codeium/windsurf/memories/global_rules.md`
      does not mention `foo`.
- [ ] Concat regen end-to-end: two rules result in a `global_rules.md`
      with both `## <name>` sections; deleting one rule and re-running
      `global sync` removes it from the concat.

### Cross-platform

- [ ] macOS, Linux: same symlink behavior.
- [ ] Windows: junctions or skip-with-fallback-to-copy; documented.
      (Decision tracked under Open Questions.)

### Manual smoke tests

- [ ] On a fresh macOS ARM machine: `sync-agents global init && sync-agents promote rule foo && sync-agents global sync` — verify `~/.claude/rules/foo.md` resolves correctly through the symlink chain.
- [ ] `sync-agents global clean` removes all global symlinks; verify `~/.agents/` is intact.
- [ ] Verify `~/.windsurf/` is **never** created by any global command.

---

## Rollout

1. One PR: `internal/agent/global.go` (new), `internal/agent/agent_test.go`,
   `internal/agent/global_test.go`, CLI wiring in `main.go`,
   Makefile `test-go` fix, `package.json` script update, bats additions.
2. README: add `promote` and `global` command table entries.
3. CHANGELOG: `feat: promote command and global sync for skills/rules/workflows`.
4. Merge → reusable workflow cuts `vX.Y.Z` tag → goreleaser + npm publish.

### Suggested implementation order (sub-steps split across PRs A–E)

The order below reflects the actual PR sequence on `feat/specs`. PR A
and PR B are landed; PR C–E are pending.

**PR A — Foundation** ✅ landed at `a84c13f`
1. Add `ResolveGlobalRoot()` resolver + `App.GlobalRoot` field +
   `$SYNC_AGENTS_GLOBAL_ROOT` env-var support. No behavior change to
   existing commands.
2. Add `Tool` struct + `Tools` registry (replaces what rev-1 sketched
   as `GlobalTargets` + string-switch `ResolveGlobalTargetDir`).
3. Add `Scope` type, `ParseScope`, `ResolveToolDir(tool, scope)`.

**PR B — CmdPromote + CmdGlobalInit** ✅ landed at `f63d143`
4. Add `DetectArtifact()` + unit tests.
5. Implement `CmdPromote` (type+name form and path-form sugar). The
   `--sync` flag is intentionally deferred to PR C because it depends
   on `CmdGlobalSync`.
6. Implement `CmdGlobalInit`.
7. CLI wiring for both commands.

**PR C — CmdGlobalSync with semantic-aware routing**
8. Add `Semantic` type + `ResolveSemantic` (frontmatter + bucket
   default).
9. Add `TargetDestination` + `ConcatTarget` + `RegenerateConcat` with
   tmp+rename atomicity.
10. Implement `CmdGlobalSync` (idempotency + repair built in from the
    start; don't bolt on later).
11. Wire `--sync` flag on `promote` to call CmdGlobalSync after
    CmdPromote.
12. CLI wiring for `global sync`.

**PR D — CmdGlobalStatus + CmdGlobalClean**
13. Implement `CmdGlobalStatus` (`[synced]` / `[missing]` / `[modified]`).
14. Implement `CmdGlobalClean`.
15. CLI wiring for both.

**PR E — Test infrastructure + release wiring**
16. Bats tests; switch them to `$SYNC_AGENTS_GLOBAL_ROOT=$BATS_TMPDIR/...`.
17. Makefile/package.json scripts; verify `npm test` runs both suites
    (AC-7, AC-8).
18. README + CHANGELOG.

*Asterisk on `--sync` in the CLI surface section above means "flag is
declared in the spec but lands in PR C, not PR B."

---

## Open Questions

- **Q1**: Should `promote` also write back a "source" header in the promoted
  file so the global copy knows its origin? (Useful for future `global pull`.)
  Recommend defer — adds schema coupling.
- **Q2** ✅ **Resolved**: `global sync` SHALL regenerate `~/.agents/AGENTS.md`
  after each sync — mirrors the project-level `AGENTS.md` pattern and gives
  Claude a global index at `~/.claude/`.
- **Q3** ✅ **Resolved**: `global init` SHALL always default to all five global
  targets (`claude,codeium,cursor,copilot,codex`). Global config is independent
  of any project's `.agents/config`.
- **Q4** ✅ **Resolved (rev 3)**: Copilot and Codex now have an explicit
  concat destination (`~/.github/copilot/instructions.md` and
  `~/.codex/instructions.md`) built into the "Semantic categories"
  routing map. Both invocable and passive artifacts merge into the same
  file, alphabetically by name, with `## <name>` headings.
- **Q6**: Concat ordering — alphabetical by name is the rev-3 default.
  Worth allowing an `order:` frontmatter key (numeric) for deterministic
  override? Defer unless we hit a real case.
- **Q7**: When a `skills/<name>/` directory contains only `SKILL.md` and
  no other files, is it considered "single-file" for the Windsurf
  workflow destination? Rev 3 says yes (path is the `SKILL.md`).
  Confirm before merge.
- **Q5**: Windows symlinks require admin or developer mode. Fall back to
  junctions or hard copies? Currently "junctions or skip-with-fallback-to-copy;
  document." Needs a firm call before merge.

---

## Future Work (out of scope, tracked here for visibility)

- **`demote` / `relocate`** — inverse of promote (global → project). Symmetric
  CLI and library design; deferred until promote ships and we see real
  usage patterns. Original detailed proposal in the pre-merge
  `SPEC-002-global-scope-promotion.md` (now removed) — see git history at
  `d036c4f` for the elaborated form, including `--copy` / `--move` /
  `--link-back` flag matrix.
- **Settings/hooks fragments** — `.claude/settings.json` JSON-patch
  promotion. Needs its own spec (SPEC-003 tentatively).
- **`global pull`** — pull a globally-updated artifact back into a project
  that has its own copy, with conflict reporting.
- **Network/remote stores** — a `.agents/` synced from a git remote.

---

## References

- Claude global config: `~/.claude/` (Claude CLI docs)
- Codeium/Windsurf global: `~/.codeium/` (Windsurf docs — local is `.windsurf/`)
- Cursor global rules: `~/.cursor/rules/` (Cursor docs)
- GitHub Copilot instructions: `~/.github/copilot/instructions.md`
- OpenAI Codex: `~/.codex/`
- SPEC-001: `go install` first-class + goreleaser
- Pre-merge sibling spec (now folded into this one): see git history at
  commit `d036c4f` for `SPEC-002-global-scope-promotion.md`.
