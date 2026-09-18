# ADR-001: Artifact custody and composition model

| | |
|---|---|
| **Status** | Proposed |
| **Version** | 0.0.1 |
| **Date** | 2026-09-18 |
| **Supersedes** | — |
| **Related** | SPEC-002 (promote / global sync), SPEC-003 (source manifest & pull, Draft), SPEC-004 (asset buckets), SPEC-007 (AGENTS.md preamble), SPEC-008 (AGENTS.md → global CLAUDE.md) |

> **Note on location.** This ADR records the founding design of a successor tool to `sync-agents`. It is written in this repository because that is where the analysis was performed and where the code it describes lives today. When the successor repository exists, this ADR relocates there and this copy becomes a pointer. SPEC-007 and SPEC-008 are referenced throughout; they currently exist only on the `feat/agents-md-preamble` branch of this repository, not on `develop`.

---

## Context

### The problem being solved

Four capabilities are wanted from a tool that manages agentic artifacts (rules, skills, workflows, subagent definitions, hooks) across many repositories:

1. **Single source of truth.** One version-controlled tree owns every artifact. Everything else is derived and disposable.
2. **Easy creation and management.** Authoring a new artifact is a command, not a filesystem ritual.
3. **Write once, use anywhere.** An artifact authored once reaches every project and every AI coding tool that consumes it.
4. **Composition / modularity.** Reusable bodies of content — "Scott's Engineering Principles" is the motivating example — can be mixed and matched to build artifacts. These bodies are *not* artifacts themselves; they are constituents.

Goals 1–3 are substantially solved by the existing `sync-agents` design. Goal 4 is not solved at all, and the one facility that gestures at it (SPEC-007's preamble) is a special case hard-coded for a single file at a single scope.

### The current model, as verified

The following is what the code does today, verified by reading it rather than by reading the documentation.

**Canonical tree and derivation.** Artifacts live in a canonical `.agents/` tree. Per-tool directories (`.claude/`, `.windsurf/`, `.cursor/`, `.github/copilot/`, `.codex/`) are *derived* from it: `sync` creates symlinks pointing back at the canonical tree. Because the derived form is a symlink and not a copy, the derived artifact cannot diverge from its source. This property is the mechanical basis of goal 1 and it is worth preserving deliberately rather than by accident.

**Scopes.** Exactly two, defined in `internal/agent/scope.go`: `ScopeLocal` (the project's `.agents/`) and `ScopeGlobal` (the user's `.agents/`, resolved through the `--global-root` → `$SYNC_AGENTS_GLOBAL_ROOT` → `$HOME/.agents` chain). `Tool.DirForScope` encapsulates the per-tool directory asymmetries — notably Windsurf, which is `.windsurf/` locally and `~/.codeium/` globally, and Copilot, which nests under `.github/`.

**Scope resolution is first-hit-wins, not layered.** `FindProjectRoot` (`internal/agent/agent.go:78-102`) walks upward from the start directory and returns the first directory containing *either* `.agents/` or `.git/`. It stops at the first hit; it does not collect ancestors. One `.agents/` tree is in play per invocation, and only one.

**Artifact types are a registry.** `internal/agent/bucket.go:58-78` defines seven buckets: `rules`, `skills` (`DirPerArtifact: true` — a skill is a directory, not a file), `workflows`, `agents` (subagent definitions, Claude-only, not created by `init`), `plans`, `specs`, and `hooks`. Adding a bucket is one registry line. `agents` already exists — subagent *definition files* are an existing capability, added by SPEC-004 Part B.

**Hooks are the exception to symlink materialization.** `hooks` are flat JSON fragments deep-merged into `.claude/settings.json` by `internal/agent/hooks.go`, not symlinked. Every other bucket resolves to "link the winner"; hooks resolve to "combine the inputs."

**Global sync iterates buckets by name.** `internal/agent/globalsync.go:392` joins the global root with each registered bucket directory explicitly; it does not walk the global root recursively. Consequence: an unregistered sibling directory inside the global root is invisible to global sync.

**Write-back paths.** Two directions exist besides the canonical → derived fan-out: `promote` (SPEC-002, local → global) and `pull`/`update` (SPEC-003, upstream repository → local, with origin metadata and ref pinning). SPEC-003 is still Draft. These are the edges where custody is ambiguous, because they move content *into* a canonical tree from outside it.

**The existing composition facility.** SPEC-007 added a preamble: a hand-authored markdown file at `<global-root>/AGENTS.preamble.md`, injected verbatim into the generated `AGENTS.md` at `index` time. SPEC-008 then mirrors the whole generated `AGENTS.md` into `~/.claude/CLAUDE.md` as a managed block. In the live global tree, `AGENTS.preamble.md` is a single 16 KB file containing both "Scott's Engineering Principles" and the "Architectural Thesis" — all-or-nothing, global-only, one file, no selection.

SPEC-007's stated non-goals are precisely the constraints goal 4 needs lifted: *"Per-project preambles"* and *"Templating, variable substitution, or conditional inclusion."*

### The incident that constrains the design

SPEC-007 exists because of a verified data-loss event. On 2026-08-10, 159 lines of hand-authored engineering principles were added directly to the generated `AGENTS.md`. The next `index` run destroyed them: 251 lines → 92 lines, confirmed empirically against a scratch copy. There was no error, no diff, and no backup. The file carried a "do not edit manually" warning that was easy to miss and was not enforced.

Any design that introduces a new class of *generated-but-committed* file recreates that trap unless it specifies how such files announce themselves and how staleness is detected. This constrains the composition decision below.

---

## Decisions

### D1. The canonical tree is the sole custodian; every other location is derived and disposable

One version-controlled `.agents/` tree owns each artifact. Per-tool directories are never authoritative and never hand-edited. Materialization remains **symlink-based** so that a derived artifact is incapable of diverging from its source.

`promote` and `pull` are **custody transfers**, not synchronizations, and must be modeled as such: each moves an artifact from one custodian to another and the destination becomes the sole owner from that moment. An artifact has exactly one custodian at any time. Bidirectional "sync" between two writable trees is explicitly not part of the model.

*Rationale.* Goal 1 fails the moment two locations are both writable and both believed authoritative. The symlink property makes "can't diverge" a mechanical guarantee rather than a discipline.

### D2. Two scopes; "group" is the enclosing directory convention, and MVP builds nothing for it

The scope model remains local and global. A group — artifacts shared by several related repositories — is achieved by placing the repositories inside an enclosing directory that holds the `.agents/` tree, and running the agent session from that enclosing directory.

This already works, and the mechanism is `FindProjectRoot` stopping at the first `.agents/` **or** `.git/`. Running from inside a member repository stops at that repository's `.git/` and never reaches the parent; running from the enclosing directory finds its `.agents/`. The behavior is a consequence of the existing walk, not a feature.

**Accepted limitation:** there is no composition of scopes. While working from the enclosing directory, a member repository's own `.agents/` is never consulted. The group tree does not layer with the repository tree; it replaces it.

Multi-group membership — a repository belonging to several named groups, with an ordered precedence list and a membership registry — is **rejected permanently**, not deferred. It introduces ambiguous precedence that must be resolved by configuration, and the enclosing-directory convention obtains a total precedence order for free from the directory hierarchy.

### D3. Ancestor-chain layering is a deferred decision, recorded not taken

The open question is narrow: resolution is currently **first-hit-wins** (`FindProjectRoot` returns one root); the alternative is **ancestor-chain layering** — collect every `.agents/` from the working directory up to the global root and resolve nearest-wins.

Layering is attractive because the directory hierarchy supplies a total order with no configuration. It is deferred because it requires two things MVP does not have: per-artifact provenance reporting (see D6) and a defined layering rule for hooks (see D5). Not decided in this ADR.

### D4. Composition: modules are constituents, not artifacts

Introduce **modules** — reusable markdown bodies that are *not* artifacts. A module:

- lives in a `modules/` bucket in the canonical tree;
- is never symlinked into a per-tool directory;
- never appears in the generated `AGENTS.md` index as a consumable;
- is never delivered to a tool standalone.

An artifact declares its constituents in frontmatter:

```markdown
---
name: engineering-standards
includes:
  - engineering-principles
  - architectural-thesis
---

Artifact-specific content follows.
```

Inclusion is **ordered concatenation, verbatim**. There is no templating, no variable substitution, and no conditional inclusion. SPEC-007 declined those deliberately and the reasoning holds: a template language turns a content-distribution tool into an evaluator, and every evaluator eventually needs debugging.

`AGENTS.preamble.md` becomes the first consumer: it decomposes into `modules/engineering-principles.md` and `modules/architectural-thesis.md`, and the preamble becomes an artifact that includes both. That single change satisfies the motivating case for goal 4.

### D5. Composition happens at authoring time; the composed output is committed

Two stages, distinct:

1. **Compose** (authoring): a `build` step expands each artifact's `includes` and writes the composed artifact into the canonical tree. The output is committed to version control.
2. **Materialize** (sync): the existing symlink fan-out, operating on composed artifacts, unchanged.

*Rationale.* Expanding at materialization time would require writing generated *copies* into per-tool directories, destroying the no-divergence property that D1 depends on. Composing at authoring time keeps materialization purely symlink-based and, because the output is committed, `git diff` shows exactly what changed in every affected artifact when a module is edited. That reviewability is the point — a module edit that silently alters fifteen artifacts is a change nobody reviewed.

**Constraint inherited from the SPEC-007 incident.** A composed artifact is a generated-but-committed file, the same category as the file that lost 159 lines. Therefore:

- every composed artifact carries a generated-file header naming its source artifact and its included modules;
- the composer records a content hash so `status` reports staleness (module edited, artifact not recomposed) as a first-class condition rather than silence;
- `lint` fails on a composed artifact whose committed content does not match a fresh composition.

The failure mode must be a loud error, never a silent overwrite.

### D6. Provenance is part of the contract, not a reporting nicety

`status` and `lint` must report, per artifact: which scope it resolved from, which modules it was composed of, and whether its composed form is current. Without this, an artifact's effective content is not traceable to its sources, and both composition (D4/D5) and any future layering (D3) become undebuggable.

### D7. On-disk format compatibility is preserved

The successor keeps the existing on-disk conventions: the `.agents/` tree layout, `AGENTS.md` as the generated index, markdown with YAML frontmatter, and the per-tool dot-directory targets. These are emerging cross-tool convention rather than any one implementation's invention, and `AGENTS.md` in particular is a format other tools target.

Compatibility costs nothing in originality — the originality of this design is the custody model (D1), the module system (D4/D5), and provenance (D6) — and it buys incremental migration plus continued use of the existing artifact trees without conversion.

### D8. The successor is a separate tool from AgentFlow

AgentFlow (`/Users/scott/github/iconify/agentflow`) is a Python/LangGraph multi-agent orchestration runtime. This tool is a Node/TypeScript artifact distribution CLI. They are not bundled.

*Rationale.* AgentFlow's own product vision lists "a collection of Claude rules or skills" under *What Agentflow Is Not*. Mechanically they share no runtime, and this tool must work for a user running only Claude Code with no AgentFlow installed. The relationship is a shared artifact **format**: AgentFlow's Agent Model carries a "Skills / policies" input, which is where materialized artifacts are consumed.

### D9. Reimplementation in Node/TypeScript

The successor is written in Node/TypeScript rather than continuing the Go implementation.

**This decision is independent of D1–D7.** Every capability decided above — modules, composition, custody modeling, provenance — is an addition to the existing Go registry and routing layers and could be built there. The reimplementation is a maintainability and ownership choice, recorded as such rather than justified by the feature set.

*Consequences.* The cross-platform build apparatus disappears: `npm/` (five platform packages), `.goreleaser.yaml`, `scripts/build-{all,platform}.js`, `scripts/lib/platforms.js`, `scripts/sync-optional-deps.js`, and `scripts/bootstrap-platform-publish.sh`. That is a genuine simplification. The cost is a Node runtime dependency and higher process startup latency, which matters where hooks invoke the CLI frequently. Package identity changes: `@brickhouse-tech/sync-agents` and the Go module path `github.com/brickhouse-tech/sync-agents` are both retired, and merges from `upstream` end permanently.

---

## Consequences

**Gained.**

- Artifacts have exactly one custodian at any time, and derived copies cannot diverge (D1).
- Reusable content is authored once and combined into many artifacts, with each combination reviewable as a diff (D4, D5).
- An artifact's effective content is traceable to its sources (D6).
- Existing `.agents/` trees migrate without conversion (D7).
- The platform build matrix is retired (D9).

**Given up or paid for.**

- Group artifacts do not layer with repository artifacts; working from an enclosing directory means the member repository's own tree is out of play (D2).
- Composed artifacts are generated files under version control, requiring recomposition discipline enforced by `lint` and a CI check (D5).
- A Node runtime becomes a prerequisite, with worse startup latency than a static binary (D9).
- The `hooks` bucket does not fit the "resolve one winner" model and needs its own merge-order rule before any layering work (D3, D5).
- Upstream merges end; every future upstream fix must be reimplemented or forgone (D9).

**Open, deliberately.**

- Ancestor-chain layering versus first-hit-wins (D3).
- Hook fragment merge order and array-valued key semantics under more than one source.
- Whether modules themselves may include other modules. Nested inclusion is not decided here; the composer must at minimum detect and reject cycles.
- Whether `pull` (SPEC-003, Draft) is carried forward at all, and if so whether an upstream-installed artifact is owned by the local tree or remains upstream-custodied.

---

## Alternatives considered

**Claude `@`-imports instead of build-time composition.** Claude Code natively resolves `@path/to/file.md` lines as includes, and the current global `CLAUDE.md` is composed this way. Rejected for three reasons: `@`-imports are Claude-only, so Cursor, Windsurf, Copilot, and Codex receive nothing; they resolve at read time, so there is no single reviewable artifact and no diff when a constituent changes; and they can only pull whole files in at the top level, so a module cannot be composed into the middle of a rule. Build-time composition produces one concrete artifact that every tool can consume identically.

**Expand includes at materialization time.** Rejected: it requires writing generated copies into per-tool directories, which destroys the symlink no-divergence guarantee that D1 rests on, and it makes every per-tool directory a second place where content can drift.

**A template language for composition** (variables, conditionals, partial application). Rejected on the same grounds SPEC-007 rejected it: it converts a distribution tool into an evaluator, and evaluators require debugging facilities that dwarf the original tool.

**Multi-group membership with a precedence list.** Rejected permanently (D2). The enclosing-directory convention obtains a total precedence order from the filesystem at no cost; named multi-membership requires configuration to disambiguate and a registry that drifts when repositories move.

**Managed fenced blocks inside artifacts** (HTML-comment markers delimiting generated regions, as `internal/agent/managedimport.go` implements). Rejected for the reason SPEC-007 gives: it makes a file partly generated and partly hand-authored, so whether an edit survives depends on where the cursor was relative to a fence. A whole-file generated/not-generated distinction is the safer contract.

**Continue on the Go implementation.** Viable on technical merits — every feature decided here fits the existing registry design. Rejected on maintainability and ownership grounds (D9).

**Bundle with AgentFlow.** Rejected (D8).

---

## Code being removed

This ADR is a design decision and removes no code on its own. The removals it commits to, landing with the SPECs that implement it:

- **`AGENTS.preamble.md` as a distinct concept**, along with the preamble-specific injection path added by SPEC-007 (`internal/agent/preamble.go` and its `index` call site, both on branch `feat/agents-md-preamble`). Superseded by the module system (D4): the live 16 KB file decomposes into `modules/engineering-principles.md` and `modules/architectural-thesis.md`, and the preamble becomes an ordinary artifact that includes them. This is a migration with a data move, not a plain deletion — the decomposition must land before the injection path is removed.
- **The cross-platform build and publish apparatus**, on reimplementation (D9): `npm/` (five platform packages), `.goreleaser.yaml`, `scripts/build-all.js`, `scripts/build-platform.js`, `scripts/lib/platforms.js`, `scripts/sync-optional-deps.js`, `scripts/bootstrap-platform-publish.sh`, and `scripts/goreleaser-check-version.sh`.
- **The Go implementation** in its entirety (`main.go`, `internal/`), superseded by the reimplementation. Not removed until the successor passes the ported acceptance suite (see below).

Reachability must be verified with tooling before each removal, not by inspection.

---

## Verification strategy

The CLI-level `bats` suites (`test/sync-agents.bats`, `test/integration-global.bats`) drive the tool from outside and are the portable behavioral contract. They are the acceptance gate for the reimplementation: the successor must pass them before the Go implementation is removed.

The Go unit tests (`internal/agent/*_test.go`, roughly 6 000 lines) encode the contract in more detail but are bound to Go internals. They are a **source** for extracting behavioral cases, not portable assets.

Behavior not covered by the `bats` suites is behavior the reimplementation will silently lose. Extending those suites to cover the current model's edges is therefore prerequisite work, not follow-up work.

---

## References

- `internal/agent/scope.go` — the two-scope model
- `internal/agent/agent.go:78-102` — `FindProjectRoot`, first-hit-wins resolution
- `internal/agent/bucket.go:58-78` — the artifact bucket registry
- `internal/agent/globalsync.go:392` — explicit per-bucket iteration of the global root
- `internal/agent/hooks.go` — JSON deep-merge materialization
- `docs/architecture/scope-and-targets.md`, `docs/architecture/global-root-resolution.md`, `docs/architecture/semantic-routing.md`
- `specs/SPEC-002-promote-global-sync.md`, `specs/SPEC-003-source-manifest-pull.md`, `specs/SPEC-004-new-asset-buckets.md`
- `specs/SPEC-007-agents-md-preamble.md`, `specs/SPEC-008-agents-md-to-global-claude-md.md` (branch `feat/agents-md-preamble`)
- AgentFlow product vision: `/Users/scott/github/iconify/agentflow/docs/agentflow-product-vision.md`
