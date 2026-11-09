# ADR-002: Sync reconciles the target instead of only adding to it

**Current version:** [0.0.1](./ADR-002-sync-reconciliation-0.0.1.md) — Proposed, 2026-09-22

Records the decision to make `sync` remove destination entries whose source artifact is gone, rather than only ever creating and repairing them, and the safety rules that govern what it is allowed to delete.

## Versions

| Version | Date | Status | Summary |
|---|---|---|---|
| [0.0.1](./ADR-002-sync-reconciliation-0.0.1.md) | 2026-09-22 | Proposed | Initial decision set: sync gains a destination pass; removal candidates are only symlinks resolving into the agents tree; removal gated by prompt / report-and-skip / `--yes`; local bucket links reconciled on the same terms; concat and managed-block targets regenerate unconditionally; `status` reports orphans first; empty directories pruned; generated index lists hand-added target entries. |
