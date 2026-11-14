# Phase 1: single-host core

The feasibility spike is complete. Implementation starts with the single-host
Phase 1 core. Phase 0 operational alerts and board rules remain separate work;
no destructive cleanup is authorized by this implementation plan.

## Sequence and acceptance

1. **Durable creation intent (implemented locally).** Persist and fsync the task,
   repository, branch, target path and pinned source SHA before creating a
   worktree. A retry registers only that exact clean checkout. Preserve and reject
   dirty, changed, unjournaled or conflicting checkouts. Retain intents alongside
   state; archive/retention cleanup is future work. No filesystem reset/deletion.
2. **Transactional service storage (implemented, verification below).** SQLite
   versioned schema and legacy import now replace JSON outside bench data; keep journal semantics and state-failure
   regressions. Status is read-only. Partial mirror clones use staged atomic publication and
   bounded abandoned directories; conflicting worktrees remain preserved.
3. **Supervised execution and fencing (implemented for delegated Linux cgroups).**
   Durable run/start/stop state, atomic cgroup placement and descendant fencing
   precede checkpoint/release. Supervisor crash, detached children, cancellation
   and full-state behavior are exercised on Linux. Cross-uid isolation and service
   provisioning remain pending; cooperative shell leases are not fenced.
4. **Host storage management (implemented, validation below).** Fixed-policy quota
   helper, reservation ledger, free-space admission, shared caches, frozen pnpm/npm
   setup, atomic mirror publication and bounded/reserved logs. Service installation,
   inode limits, TOML hooks and automatic collection remain pending.
5. **benchd and local CLI (API implemented; archive pending).** Kernel peer uid
   authorization, bounded durable request receipts, strict pinned treaty vectors,
   redacted status and crash reconciliation. See [service verification](service.md).
   Service installation and checkpoint-verified archive/retention remain pending.
6. **Suite pilot.** Charter ownership change and treaty API/event contracts before
   adapters; one opt-in board and a clean-checkout verification path. No production
   tag or deployment is part of this local implementation turn.

Phase 2 adds visibility and locality. Phase 3 adds additional hosts and stronger
isolation. Neither is needed to start the Phase 1 core.


## Full implementation scope

The active objective includes every phase in sketchbook's SuperBench proposal.
A core checkpoint does not constitute completion of a phase or the product.

| Phase | Required outcome | Current evidence |
|---|---|---|
| 0 | Host disk alert, reviewed cleanup dry-run, code-stage branch/checkpoint rules | Not implemented by this repository; inspect estate and board settings before changes |
| 1 | Standalone CLI and host service; durable state/leases; checkpoints/recovery; quotas, shared mirrors/stores; verified archive/reap | CLI, journals, SQLite, fenced Linux execution and host storage tested; local service API added; installation, archive/reap and integration pending |
| 2 | Suite adapters, card panel, locality scoring, witness events; read-only web, file/diff/log/timeline views, terminal attach and MCP | Pending; charter/treaty contracts precede consumers; visual mockups before UI |
| 3 | Remote editor launch/browser IDE; second-host recovery drill; per-bench isolation; Salaam layout and execution-host integration | Pending |

For each checkpoint: run required checks, commit in this repository, verify remote
PR state before pushing, and record concrete verification and remaining limits.
Production tags remain operator-only. Cleanup requires a reviewed dry-run list;
keep that approval distinct from permission to implement the product.

## Published checkpoints

- `c874bf6`: initial CLI, journaled clean-worktree adoption, failure experiments
  and full phase checklist. Local checks and Forge push CI passed.
- `ea47c2f`: versioned SQLite state, legacy import, rollback and read-only status;
  local race tests, Linux failure tests and Forge CI passed; PR #1 is open.
- `c1def55`: supervised Linux execution; local checks, staged cgroup tests and Forge
  CI passed; PR #2 is stacked on #1.
- Current storage slice: quota helper, admission, frozen setup, atomic mirrors and
  log budgets; local and staged verification recorded in docs/storage.md.
  `32b3859`, stacked PR #3.
- Local daemon slice: strict local API, authenticated Unix peers, durable receipts
  and remote CLI. Treaty and charter are proposed separately. Installation,
  history and archive/reap are next.
