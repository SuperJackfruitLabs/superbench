# Phase 1: single-host core

The feasibility spike is complete. Implementation starts with the single-host
Phase 1 core. Phase 0 operational alerts and board rules remain separate work;
no destructive cleanup is authorized by this implementation plan.

## Sequence and acceptance

1. **Durable creation intent (implemented locally).** Persist and fsync the task,
   repository, branch, target path and pinned source SHA before creating a
   worktree. A retry registers only that exact clean checkout. Preserve and reject
   dirty, changed, unjournaled or conflicting checkouts. Retain intents alongside
   state; reviewed archive/reap is implemented; approved automatic log retention is implemented; broader collection remains future work. Creation never resets or deletes conflicting files.
2. **Transactional service storage (implemented, verification below).** SQLite
   versioned schema and legacy import now replace JSON outside bench data; keep journal semantics and state-failure
   regressions. Status is read-only. Partial mirror clones use staged atomic publication and
   bounded abandoned directories; conflicting worktrees remain preserved.
3. **Supervised execution and fencing (implemented for delegated Linux cgroups).**
   Durable run/start/stop state, atomic cgroup placement and descendant fencing
   precede checkpoint/release. Supervisor crash, detached children, cancellation
   and full-state behavior are exercised on Linux. Cross-uid isolation remains pending; delegated service installation is verified; cooperative shell leases are not fenced.
4. **Host storage management (implemented, validation below).** Fixed-policy quota
   helper, reservation ledger, free-space admission, shared caches, frozen pnpm/npm
   setup, atomic mirror publication and bounded/reserved logs. A staged Linux service installer is now verified separately. Byte and inode limits are enforced and verified.
   Explicit TOML setup hooks are implemented; automatic acquisition hooks and
   broader cache/journal collection remain pending.
5. **benchd and local CLI (API and reviewed archive/reap implemented).** Kernel peer uid
   authorization, bounded durable request receipts, strict pinned treaty vectors,
   redacted status and crash reconciliation. See [service verification](service.md).
   Service installation has a verified Linux payload; checkpoint-verified
   archive/reap and approved automatic log retention are verified separately.
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
| 1 | Standalone CLI and host service; durable state/leases; checkpoints/recovery; quotas, shared mirrors/stores; verified archive/reap | CLI, journals, SQLite, fenced Linux execution and host storage tested; local service API added; installer and reviewed archive/reap verified; reviewed log/history collection and approved automatic log retention implemented; explicit TOML setup hooks implemented; cache/journal collection, automatic acquisition hooks and integration pending |
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
  and remote CLI. Treaty and charter are proposed separately; service PR #4
  passed local and Forge CI plus staged Linux recovery tests. History and
  automatic retention remained pending at that checkpoint.
- Linux service installation: staged payload, private stable account, delegated
  execution and fixed sudo helper verified with a disposable systemd unit.
  No production service was installed or started.
- Reviewed archive/reap: exact file manifests and durable retry intents; managed
  reservations retire only after zero XFS usage. Retained logs, receipts and
  journals still require their own reviewed collection lifecycle.
- Inode quotas: explicit per-project/host counts, direct-root legacy migration,
  pinned state metadata and target revalidation before managed acquisition.
  Actual XFS zero-byte-file limits, migration, overcommit and retirement are verified.

- Approved automatic log retention: explicit policy/initial-batch approval,
  bounded periodic daemon collection, exact persisted batch recovery and a
  disabling gate. Local/Linux required checks, actual cgroup/daemon drills and
  a disposable systemd installation passed. No installed policy was enabled.

- Explicit repository setup: bounded `.bench/config.toml`, durable once receipts,
  per-attempt each hooks and pre-effect invalidation when configuration changes.
  Required local/Linux checks, actual daemon/cgroup setup and execution failure
  drills passed. Automatic acquisition hooks remain pending; see [setup](setup.md).


## Remaining retention work

Reviewed log collection and history are implemented in [retention.md](retention.md),
including protected held/failed evidence and durable interruption recovery.
Approved automatic log policy execution is implemented in [automatic-retention.md](automatic-retention.md); broader collection remains pending. Use a reviewed candidate plan and verify the same files/ownership
before collection; never treat age as proof that a writer stopped. Shared cache
and abandoned-clone collection need the same active-writer and namespace checks.

Receipt collection must not make an old request ID executable again. Existing
v1 opaque IDs need durable tombstones when their response payload is retired.
Sustainable bounded receipt collection needs a producer-first treaty extension
with a bounded retry/freshness contract and a persisted monotonic watermark;
changing the retry window must never expire a bench lease or release a writer.
Do not implement collection by simply deleting completed receipt rows.
