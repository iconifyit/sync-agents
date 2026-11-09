# `sync-agents global sync`

Fan the user's global `~/.agents/` tree out to per-tool global
directories — `~/.claude/`, `~/.codeium/`, `~/.cursor/`,
`~/.github/copilot/`, `~/.codex/` — using semantic-aware routing.

## Synopsis

```text
sync-agents global sync [--targets t1,t2] [--dry-run] [--force] [--global-root PATH]
```

## What it does

For every artifact under `~/.agents/{rules,skills,workflows}/` and
every registered tool, the sync:

1. **Resolves the artifact's semantic** (`invocable` or `passive`)
   via YAML frontmatter or, when absent, the bucket default. See
   [Semantic routing](../architecture/semantic-routing.md).
2. **Resolves the per-tool destination** for that (semantic, tool)
   pair using the routing table in SPEC-002.
3. **Performs the appropriate filesystem operation**:
   - Per-artifact **symlink** (Claude, Cursor, Windsurf invocables)
   - Append to a **concat batch** (Windsurf passive, Copilot, Codex)
   - **Skip with warning** (multi-file invocable skills targeting
     Windsurf workflows; passive skill dirs targeting Claude)
4. **Regenerates concat targets** at the end — one atomic
   tmp+rename write per concat file. If the new content equals the
   existing content (byte-identical), the file is left alone
   preserving its mtime.

## Per-tool routing reference

| Tool | Invocable destination | Passive destination |
|---|---|---|
| `claude` | `~/.claude/skills/<name>` → the skill directory (skill) or `~/.claude/commands/<name>.md` (rule/workflow) | `~/.claude/rules/<name>.md` |
| `codeium` | `~/.codeium/windsurf/global_workflows/<name>.md` (single-file only) | concat → `~/.codeium/windsurf/memories/global_rules.md` |
| `cursor` | `~/.cursor/rules/<name>.md` | `~/.cursor/rules/<name>.md` (same — Cursor doesn't distinguish) |
| `copilot` | concat → `~/.github/copilot/instructions.md` | same concat |
| `codex` | concat → `~/.codex/instructions.md` | same concat |

Claude links a skill's whole directory, not just its `SKILL.md`. Claude reads a skill from `~/.claude/skills/<name>/`, so the skill's supporting files (scripts, templates, references) are reachable only when that path is the skill directory itself. The other symlink tools take one markdown file per artifact and link the skill's `SKILL.md`.

A real `~/.claude/skills/<name>/` directory whose only entry is a `SKILL.md` link to the skill (the layout earlier versions produced) is replaced by the directory link on the next sync. A directory holding anything else is left untouched and reported, like any other non-symlink in the way; `--force` moves it aside.

A link at `~/.claude/skills/<name>` that points at a directory outside the global root (for example straight at a working copy) is the user's, the same rule `global clean` applies. Sync leaves it in place and `global status` reports it as `[not-a-symlink]`; `--force` replaces it and prints its old target.

Skipped cases:

- **Multi-file invocable skill → Codeium**: Windsurf workflows are
  single `.md` files, so a skill dir with `SKILL.md` plus supporting
  files can't be a workflow without flattening. Skipped for codeium
  only with a warning; the skill still syncs to other tools.
- **Passive skill → Claude**: a directory marked `invocable: false`
  has no clean Claude destination (Claude's passive surface is a
  single `rules/*.md` file). Skipped with a warning. Decide whether
  to split the skill into rules or accept the gap.

## Idempotency

Re-running `global sync` on an unchanged tree:

- **Symlinks** that already point at the canonical artifact are
  left alone.
- **Symlinks** that point at a different (or broken) target are
  silently repaired and logged as `[repair]`.
- **Concat files** whose newly-computed content byte-equals the
  existing content are not rewritten — `mtime` is preserved so
  external watchers don't fire spuriously.

If everything is current, the command exits 0 with summary lines
like `… already current (N entries)`.

## Conflicts

If a **non-symlink** file or directory exists at a per-tool symlink
destination, the sync refuses to overwrite it without `--force`. The
warning names the path so you can investigate.

With `--force`, the conflicting file is moved to
`<path>.replaced-by-sync-agents` (so it's recoverable) before the
fresh symlink is placed. A conflicting directory, such as a Claude
skill dir, is moved to `<tool dir>/.replaced-by-sync-agents/<bucket>/<name>`
(for example `~/.claude/.replaced-by-sync-agents/skills/<name>`), outside
the directory the tool scans, so Claude does not load the backup as a
second copy of the skill. If a backup is already at that path, the sync
stops for that destination and names it.

Concat files don't go through the conflict check — they're
sync-agents-owned by design. Any user edits to a generated concat
will be overwritten on the next sync; that's why the banner says
"do not edit by hand."

## Flags

| Flag | Default | Effect |
|---|---|---|
| `--targets <list>` | all | Comma-separated tool IDs to sync. Aliases honored: `windsurf` resolves to `codeium`. Unknown names produce a warning and are skipped. |
| `--dry-run` | false | Print every planned operation prefixed with `[dry-run]`. No filesystem writes. |
| `--force` | false | Replace non-symlinks in the way (files saved as `*.replaced-by-sync-agents`; directories under `<tool dir>/.replaced-by-sync-agents/`). |
| `--global-root <path>` | `$HOME/.agents` | Override the global root. See [Global root resolution](../architecture/global-root-resolution.md). |

`--dry-run` and `--force` are persistent flags inherited from `rootCmd`.

## Examples

```bash
# Standard sync of everything.
sync-agents global sync

# Only sync Claude and Cursor.
sync-agents global sync --targets claude,cursor

# Windsurf alias works.
sync-agents global sync --targets windsurf

# Preview without writing.
sync-agents global sync --dry-run

# Use a temp root (test rig).
sync-agents global sync --global-root /tmp/.agents

# Compose with promote in one shot.
sync-agents promote rule security --sync
```

## Promote-and-sync composition

`sync-agents promote ... --sync` runs `global sync` immediately after
a successful promote. Use `--sync-targets <list>` on the `promote`
command to limit which tools are synced in that follow-up:

```bash
# Promote a rule and immediately fan it out to just Claude.
sync-agents promote rule security --sync --sync-targets claude
```

When `--sync` is omitted, `--sync-targets` is ignored.

## Exit codes

- `0` on success or a clean dry-run. Skipped artifacts produce
  warnings but do not change the exit code.
- non-zero if `~/.agents/` doesn't exist (run `global init`) or if
  a filesystem error halts the whole sync (rare).

Per-tool failures (concat regen error, symlink creation error)
produce warnings but do not abort the run — partial progress is
preferable to all-or-nothing for fan-out operations.

## What `global sync` does NOT do

- It does not pull artifacts from upstream repos — that's
  [SPEC-003](../../specs/SPEC-003-source-manifest-pull.md).
- It does not regenerate per-project sync targets (the local
  `.claude/`, `.windsurf/`, etc.). Use the existing `sync-agents
  sync` command for project scope.
- It does not modify or delete content inside `~/.agents/`. The
  canonical tree is read-only from sync's perspective.

## See also

- [SPEC-002 §Requirement: Global sync](../../specs/SPEC-002-promote-global-sync.md)
- [SPEC-002 §Requirement: Semantic-aware routing](../../specs/SPEC-002-promote-global-sync.md)
- [Semantic routing](../architecture/semantic-routing.md)
- [Scope and target directories](../architecture/scope-and-targets.md)
- [`sync-agents promote`](./promote.md)
- [`sync-agents global init`](./global-init.md)
- `internal/agent/globalsync.go`, `internal/agent/destination.go`,
  `internal/agent/concat.go`, `internal/agent/semantic.go` —
  the implementation.
