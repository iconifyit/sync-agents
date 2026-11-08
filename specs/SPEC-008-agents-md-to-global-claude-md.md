---
id: SPEC-008
title: "Mirror AGENTS.md into the global CLAUDE.md during global sync"
status: Draft
owner: scott
created: 2026-08-11
updated: 2026-08-11
related: SPEC-002, SPEC-007
---

# [SPEC-008] Feature: mirror AGENTS.md into the global CLAUDE.md

## Overview

`sync-agents global sync` writes a managed `@`-import block of passive rules into `~/.claude/CLAUDE.md` and nothing else. SPEC-007 made `index` inject `AGENTS.preamble.md` into `AGENTS.md`, but that content never reaches the global `CLAUDE.md`. This SPEC closes the gap: `global sync` mirrors the whole generated `AGENTS.md` into `CLAUDE.md` as a second managed block.

## Motivation

Editing `AGENTS.preamble.md` updated `AGENTS.md` and appeared to do nothing. Sessions outside the agents repo kept loading principles from a hand-pasted copy that had been sitting in `~/.claude/CLAUDE.md` since before the preamble existed — stale by several edits, including a formatting fix and two whole new sections.

There was no supported path from the preamble to the global file. The only workaround was to hand-maintain a duplicate, which is what had silently drifted.

## Goals

- Editing `AGENTS.preamble.md` then running `global sync` updates `~/.claude/CLAUDE.md`.
- The global `CLAUDE.md` is the generated `AGENTS.md` plus the rule `@`-imports.
- Hand-written content outside the managed blocks is preserved.
- Links that were repo-relative in `AGENTS.md` still resolve from the global file.

## Non-Goals

- Changing what `index` generates. `AGENTS.md` is the input here, unmodified.
- Mirroring to tools other than Claude. Other targets consume `AGENTS.md` in their own project trees.
- Deduplicating the preamble. `AGENTS.md` already contains it (SPEC-007); mirroring `AGENTS.md` alone carries it exactly once.

## Design

### A second managed block

`CLAUDE.md` gains `<!-- sync-agents:agents-md:start/end -->` alongside the existing `claude-imports` pair. Two independent regions rather than one file-wide rewrite: content outside both is preserved, matching the property `global sync` already guaranteed.

`replaceManagedBlock` was generalized to `replaceBlockBetweenMarkers(existing, newBlock, start, end)`; the import path passes its own markers and is behaviorally unchanged.

### Locating AGENTS.md

The global root is conventionally a symlink (`~/.agents -> <repo>/.agents`) and `AGENTS.md` lives in the repo that owns the tree. Resolving the parent **lexically** would look beside the symlink — `$HOME/AGENTS.md` — and find nothing, silently disabling the mirror. `ResolveGlobalAgentsMDPath` calls `filepath.EvalSymlinks` first, then takes the parent.

A missing `AGENTS.md` returns `""` and the mirror is skipped. Opt-in, absence is not an error — same contract as the preamble in SPEC-007.

### Absolutizing links

`AGENTS.md` indexes artifacts as `](.agents/rules/x.md)`, correct relative to the agents repo. Mirrored verbatim into `~/.claude/CLAUDE.md` those resolve to `~/.claude/.agents/rules/x.md` and dangle. `AbsolutizeAgentsMDLinks` rewrites the `](.agents/` prefix to the absolute repo path. Only that prefix is touched — external URLs and other relative links are left alone.

## Requirements

### Requirement: Mirror on global sync

#### Scenario: Preamble edit propagates
- **GIVEN** `AGENTS.preamble.md` is edited and `index` has regenerated `AGENTS.md`
- **WHEN** `global sync --targets claude` runs
- **THEN** `~/.claude/CLAUDE.md` contains the edited content
- **AND** it also contains the rule `@`-import block

#### Scenario: Content outside the blocks survives
- **GIVEN** `CLAUDE.md` has hand-written text outside both marker pairs
- **WHEN** the mirror runs
- **THEN** that text is unchanged

#### Scenario: Stale content is replaced, not appended
- **GIVEN** a previous mirror wrote older content
- **WHEN** `AGENTS.md` changes and the mirror runs again
- **THEN** the old content is gone and the new content appears exactly once

#### Scenario: Idempotent
- **WHEN** the mirror runs twice with no change in between
- **THEN** the second run reports no change and the bytes are identical

#### Scenario: Links resolve
- **GIVEN** `AGENTS.md` contains `](.agents/rules/x.md)`
- **WHEN** the mirror runs
- **THEN** the mirrored link is absolute and no `](.agents/` remains

#### Scenario: No AGENTS.md is not an error
- **GIVEN** the global tree has no `AGENTS.md`
- **WHEN** `global sync` runs
- **THEN** the mirror is skipped, no error, no `CLAUDE.md` created by the mirror

#### Scenario: Symlinked global root
- **GIVEN** the global root is a symlink into the agents repo
- **THEN** `AGENTS.md` is found beside the resolved `.agents/`, not beside the symlink

### Requirement: Dry-run safety

#### Scenario: Dry run does not write
- **WHEN** `global sync --dry-run` runs
- **THEN** `CLAUDE.md` is unchanged and the intended mirror is reported

### Requirement: Blocks coexist

#### Scenario: Neither block clobbers the other
- **WHEN** both blocks are written and one is regenerated
- **THEN** the other survives intact

## Code being removed

None. Purely additive. `replaceManagedBlock` is generalized, not deleted, and its behavior is unchanged.

## Verification plan

- Unit tests for path resolution through a symlinked root, link absolutization, mirroring, idempotence, edit propagation, block coexistence, and the absent-AGENTS.md case.
- End-to-end against a disposable tree mirroring the production topology (repo with `AGENTS.md` + `.agents/`, `home/.agents` symlink), never a live tree.

## Open questions

None.
