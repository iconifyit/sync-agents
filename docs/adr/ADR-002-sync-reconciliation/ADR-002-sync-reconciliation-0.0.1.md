# ADR-002: Sync reconciles the target instead of only adding to it

| | |
|---|---|
| **Status** | Proposed |
| **Version** | 0.0.1 |
| **Date** | 2026-09-22 |
| **Supersedes** | — |
| **Related** | SPEC-002 (promote / global sync), SPEC-004 (asset buckets), SPEC-007 (AGENTS.md preamble), SPEC-008 (AGENTS.md → global CLAUDE.md), ADR-001 (artifact custody and composition) |

---

## Context

### The defect

`sync` only ever adds and repairs. It reads the canonical `.agents/` tree, ensures every artifact it finds has a matching destination, and stops. It never inspects the destination to ask whether anything there no longer has a source. Deleting an artifact therefore leaves its symlink behind, dangling, permanently.

The pattern is **ensure present, never ensure absent**, and it appears in three places. All three were reproduced against a disposable sandbox using a binary built from `feat/agents-md-preamble` @ ac72b57. Line references below are against `develop`, where this ADR lives; the behavior is identical on both branches.

**1. Global per-artifact symlinks.** `CmdGlobalSync` (`internal/agent/globalsync.go:55`) loops over `DiscoverArtifacts(root)`; `applySymlinkDestination` (`globalsync.go:284`) handles exactly three cases — nothing at the destination, a symlink at the destination, a non-symlink at the destination. There is no fourth pass over the destination tree.

Reproduction: four artifacts synced, then `doomed.md`, `doomed-agent.md` and the `demo-skill/` source directory deleted. The next run reported `syncing 1 artifact(s) to 1 tool(s)` and `global sync complete` with no warning, leaving three dangling symlinks plus an empty `skills/demo-skill/` directory.

This is not Claude-specific. Symlink destinations exist at global scope for **claude** (rules, commands), **cursor** (rules) and **codeium** (global_workflows); codex and copilot receive concat output only. Repeating the deletion against all five targets left five orphans across three tools.

**2. Local per-bucket symlinks.** `CmdSync` (`internal/agent/agent.go:330`) links whole bucket directories — `.claude/rules -> ../.agents/rules` — creating a link when the source directory exists and having no branch for the source directory having gone away. Local sync is immune to *per-file* orphans, because the destination is one link to a live directory; it is not immune to bucket deletion.

Reproduction: local `init` + `sync`, then `rm -rf .agents/workflows`, then `sync` again. The second run printed `Sync complete.` and left `.claude/workflows -> ../.agents/workflows` dangling in all four local tool directories.

**3. Concat targets that lose their last contributor.** Codex and Copilot cannot consume a directory of separate rule files, so sync concatenates artifacts into a single file per target. `concatBatches` accumulates entries only for artifacts that still exist, and `RegenerateConcat` runs once per accumulated batch — so a target whose contributor set becomes empty is never visited and keeps its previous content indefinitely.

Reproduction: one rule plus one workflow synced; the rule then deleted (so artifacts still exist and sync does not bail). `.codeium/windsurf/memories/global_rules.md`, which takes passive rules only, was **not regenerated** — no log line for it — and still read `## only-rule`. `~/.claude/CLAUDE.md` still carried an `@`-import pointing at the deleted rule's now-broken symlink. The targets that still had a contributor (`.codex/instructions.md`, `.github/copilot/instructions.md`) regenerated correctly, 2 entries → 1.

A stricter variant: delete every artifact and `CmdGlobalSync` early-returns at `globalsync.go:91` with `no artifacts under ...; nothing to sync`, freezing every concat file and the `CLAUDE.md` managed block at their previous contents.

### Nothing surfaces the problem

`CmdGlobalStatus` walks the same `DiscoverArtifacts` set, so an orphan — which by definition has no source artifact — never produces a row. With three dangling symlinks present, status reported a single `[synced]` line and nothing else. `lint` does not cover it either. The tool reports a healthy state while the destination is broken.

`CmdGlobalClean` (`globalclean.go:47`) does walk the destination and would remove the orphans, but it removes every managed link including live ones. `clean` followed by `sync` is currently the only remedy.

### A second gap, found while investigating

A rule placed by hand directly in `~/.claude/rules/` is left correctly untouched by sync, but it is **not** added to the generated `@`-import block, because that block is built from the source-driven artifact loop. Verified in the sandbox: a hand-written `some-other-rule.md` survived the sync and received no import, so Claude never loads it. The file exists and does nothing.

This is absent behavior rather than broken behavior, and it is recorded here as a separate decision (D8).

### Current live state

The user's live tree is clean at time of writing: 35/35 rules and 29/29 skills reconcile between `~/.claude` and `~/.agents`, and the only extra entries are two `--force` backup files under `~/.claude/agents/`. The defect is latent and reproducible on demand rather than currently manifesting.

### Not specified, not tested

No SPEC states a deletion-reconciliation requirement for `sync`, and no Go or bats test covers orphan removal. Nothing regressed — the behavior was never written.

---

## Decisions

### D1. Sync reconciles the target: ensure present **and** ensure absent

`sync` and `global sync` gain a destination pass. After the existing source-driven pass, they walk the managed destination directories, identify entries that sync-agents owns but that no longer have a backing source, and offer to remove them under the gate in D3.

*Rationale.* A tool whose stated purpose is to make the target match the source cannot be correct while it can only ever add. The reconciliation loop is the whole fix; everything below is scoping and safety.

### D2. Ownership test: a symlink whose **resolved** target lies inside the agents tree

An entry is a removal candidate only when both hold:

1. it is a symlink — never a regular file, never a directory; and
2. following the link text and resolving symlinks in the path yields a location inside the canonical agents tree.

Everything else is left alone, with no warning noise. Specifically:

- **Hand-written files in the target are never candidates.** A user must be able to keep their own rules, skills, and agents alongside the managed ones. Regular files are categorically excluded.
- **A broken symlink pointing somewhere other than the agents tree is never a candidate**, even though it is useless. It is not ours. Tidying it is not worth the risk of deleting something a user created.

**The comparison must be on resolved paths, not on link text.** `ResolveGlobalRoot` does not resolve symlinks (`absOrSelf` is `filepath.Abs`, `globalroot.go:107`), and on the user's machine the global root `/Users/scott/.agents` is itself a symlink into `/Users/scott/github/@agents/.agents`. Five older whole-directory skill links spell the same location the other way — `~/.claude/skills/adr-authoring -> /Users/scott/github/@agents/skills/adr-authoring` — and fail the existing lexical prefix test in `symlinkPointsInto` (`globalclean.go:357`).

This was verified against the real tree with `global clean --dry-run` (no writes): it listed 24 of 29 skills for removal — exactly the current-style `<name>/SKILL.md` links — and silently skipped all 5 old-style links. 24 + 5 = 29. A reconciliation pass reusing that predicate unchanged would inherit the same blind spot, so `symlinkPointsInto` must resolve both sides before comparing. `os.Readlink` is retained rather than `os.Stat` because the predicate must work on links that are already broken.

### D3. Removal is gated three ways

| Context | Behavior |
|---|---|
| Interactive terminal | List the candidates, prompt y/n, remove on yes |
| Non-interactive (no TTY) | List the candidates, remove nothing |
| `--yes` passed | Remove without prompting |

`--dry-run` continues to mean "print the plan, write nothing," and composes with all three.

*Rationale.* Deletion is irreversible and this is the first version of the logic, which is where it is most likely to be wrong. Sync also runs unattended — from the `add-rule` skill, from hooks, potentially from CI — where a blocking prompt would hang and a silent delete would be unwatched. Report-and-skip is the safe default for those; `--yes` makes the automatic behavior available to anyone who wants it, as an explicit choice.

### D4. Local `sync` reconciles bucket links on the same terms

The local per-bucket links get the same treatment under the same ownership test and the same gate. A bucket link whose source directory no longer exists is a removal candidate.

### D5. Concat and managed-block targets regenerate unconditionally

Regeneration is driven by the set of *targets*, not by the set of surviving contributors. A target whose contributor set is empty is still visited and still rewritten, with an explicit `_no items_` placeholder in place of the section body rather than the previous run's text.

The early return at `globalsync.go:91` is removed: an empty source tree is a valid state that must still be propagated to the destination, not a reason to skip the run.

This covers the concat files for codex, copilot and codeium, and the managed `@`-import block in `~/.claude/CLAUDE.md`.

### D6. `status` reports orphans as a first-class state

`global status` (and the local equivalent) gain a destination pass and report each orphan explicitly. Status must be able to tell the truth about a broken destination; today it reports health while the target is broken.

This is the piece to build and ship first. It is read-only, and it lets the ownership test in D2 be validated against the real tree before anything is authorized to delete.

### D7. Empty directories are pruned after removal

Removing a skill's `SKILL.md` link leaves an empty `<name>/` directory behind. Directories that become empty as a result of a removal in this pass are pruned, walking upward and stopping at the per-tool root. Directories that were already empty before the pass are not touched.

### D8. The generated index lists everything present in the target

The `@`-import block and the generated index cover every artifact actually present in the destination — hand-added regular files included — not only those derived from the canonical tree.

*Rationale.* A rule a user drops into `~/.claude/rules/` currently exists but never loads, which is a silent failure with no signal. Listing what is present makes the destination self-describing.

This composes with D2 without conflict: a hand-added file is a regular file, so it is listed but is never a removal candidate. The two rules point the same way — sync reads everything in the target, and deletes only what it put there.

*This is new behavior, not a defect.* It should land after D1–D7 and may be split into its own change.

---

## Consequences

**Gained.**

- Deleting an artifact from the canonical tree actually removes it everywhere (D1, D4).
- The destination can no longer silently diverge from the source (D1, D5).
- `status` tells the truth about the destination (D6).
- Hand-added artifacts in the target both survive and take effect (D2, D8).

**Given up or paid for.**

- `sync` becomes a command that can delete. Its risk profile changes permanently, and the gate in D3 is the only thing standing between a logic error and data loss.
- Unattended runs no longer self-heal by default. Orphans persist until someone runs sync interactively or passes `--yes` (D3).
- A broken symlink pointing outside the agents tree is never cleaned up by this tool, by choice (D2).
- Users who relied on an artifact surviving in the target after its source was deleted lose that behavior. This is the intended correction, but it is a behavior change.

**Open, deliberately.**

- Whether `--yes` should be inferable from an environment variable for CI, or must always be an explicit flag.
- Whether D8's listing should mark hand-added entries distinctly in the generated index so their origin is visible.
- Whether `clean` should also adopt the resolved-path ownership test from D2. It has the same blind spot today, and fixing it there widens what `clean` deletes — which is a separate decision from this one.

---

## Alternatives considered

**A manifest of what sync wrote ("the receipt").** Record every destination created on each run; on the next run, remove anything on the old list that is not on the new one. Rejected: it introduces a second source of truth that must itself be kept correct, and a bootstrap problem for the first run after upgrade. The canonical tree already answers "what should exist," and the symlink target already answers "who created this" — deriving the answer from those two needs no new state. The manifest's one genuine advantage is catching a link in a location sync no longer writes to after a routing change; that is what `clean` is for.

**Modeling this on the hooks ownership state file** (`hooks.go:108-125`, `~/.agents/.sync/claude-hooks-state.json`). Rejected as a template. Hooks needs recorded ownership because hook entries are merged into a shared JSON file where nothing on disk indicates who wrote which entry. A symlink carries its provenance in its own target. Different problem, different solution.

**Removing every broken symlink found in the managed directories.** Tidier, and a broken symlink helps nobody. Rejected: it deletes entries sync-agents never created. The convenience does not justify touching a user's own files, particularly under `--yes` where no one sees the list.

**Keeping the existing lexical prefix test in `symlinkPointsInto`.** Rejected on the verified evidence above: it misses 5 of 29 live skill links because the global root is itself a symlink and the older links spell the destination the other way.

**Leaving `clean` + `sync` as the remedy.** Rejected: it deletes every managed link including live ones, it is manual, and nothing tells the user when it is needed because `status` cannot see the problem.

**Automatic removal with no gate.** Rejected for the first version of the logic. Reconsider once D6 has run against real trees for a while and the ownership test has demonstrated it identifies exactly the right set.

---

## Code being removed

Most of this decision is additive — a new destination pass alongside the existing source pass. The removals it commits to:

- **The empty-source early return** in `CmdGlobalSync` (`internal/agent/globalsync.go:91`, the `no artifacts under ...; nothing to sync` branch). Superseded by D5: an empty source is a state to propagate, not a run to skip. The surrounding `hasClaudeTarget` / `dirExists(hooks)` special case exists only to guard that early return and goes with it.
- **The lexical prefix comparison inside `symlinkPointsInto`** (`internal/agent/globalclean.go:357`), replaced by a resolved-path comparison per D2. The function is retained and its `os.Readlink` behavior is deliberately preserved; only the comparison changes. Note this changes `CmdGlobalClean`'s behavior as a side effect — it will begin removing the 5 old-style skill links it currently skips — which is listed as an open question above and must be decided before the change lands.

No module, export, or file becomes dead as a result of this ADR. Reachability must be confirmed with tooling rather than inspection before either removal lands.

---

## Verification strategy

**Order of work.** D6 first, alone, and confirmed against the real tree before any deletion path is written. It is read-only, and it is the cheapest way to prove the D2 ownership test selects exactly the intended set — in particular that it catches the 5 old-style skill links that the current predicate misses, and that it excludes every hand-written file.

**Regression tests for each reproduction above**, written to fail before the fix:

- global: delete a rule, a skill and an agent; assert no dangling symlink survives in any tool directory
- global: assert a hand-written regular file in the target survives untouched
- global: assert a broken symlink pointing outside the agents tree survives untouched
- global: assert an old-style differently-spelled link is correctly identified as owned
- local: delete a whole bucket; assert the bucket link does not survive
- concat: delete a target's last contributor; assert the file is regenerated with the placeholder
- concat: delete every artifact; assert every concat target and the managed block are still rewritten
- skills: assert the emptied `<name>/` directory is pruned
- gate: assert non-interactive runs remove nothing, and that `--yes` removes without prompting

**Acceptance gate.** The CLI-level `bats` suites (`test/sync-agents.bats`, `test/integration-global.bats`) drive the tool from outside and are where the gate behavior in D3 belongs, since it is terminal-dependent. Note that these suites have not been run in the course of this investigation; whether they currently pass on `develop` is unverified and should be established before relying on them.

---

## References

- `internal/agent/globalsync.go:55` — `CmdGlobalSync`, the source-driven loop
- `internal/agent/globalsync.go:91` — the empty-source early return
- `internal/agent/globalsync.go:284` — `applySymlinkDestination`, create/repair only
- `internal/agent/agent.go:330` — `CmdSync`, local per-bucket linking
- `internal/agent/globalstatus.go:127` — `CmdGlobalStatus`, source-driven and therefore blind to orphans
- `internal/agent/globalclean.go:47` — `CmdGlobalClean`, the existing destination walk
- `internal/agent/globalclean.go:357` — `symlinkPointsInto`, the ownership predicate
- `internal/agent/globalroot.go:107` — `absOrSelf`, which does not resolve symlinks
- `internal/agent/hooks.go:108-125` — the recorded-ownership reconciliation, considered and rejected as a template
- ADR-001 — artifact custody and composition, which this decision's custody model is consistent with
