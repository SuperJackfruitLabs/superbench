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
   setup, atomic mirror publication, acquisition-time remote-tracking refresh and bounded/reserved logs. A staged Linux service installer is now verified separately. Byte and inode limits are enforced and verified.
   Explicit, automatic acquisition and initial creation/recovery setup are implemented
   on Linux; laptop preparation and broader cache/journal collection remain pending.
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
| 1 | Standalone CLI and host service; durable state/leases; checkpoints/recovery; quotas, shared mirrors/stores; verified archive/reap | CLI, journals, SQLite, fenced Linux execution and host storage tested; local service API added; installer and reviewed archive/reap verified; reviewed log/history collection and approved automatic log retention implemented; explicit/automatic acquisition setup hooks implemented; creation/recovery warm-up verified on Linux; laptop preparation, multi-repository layout, cache/journal collection, configuration and integration pending |
| 2 | Suite adapters, card panel, locality scoring, witness events; read-only web, file/diff/log/timeline views, terminal attach and MCP | Pending; charter/treaty contracts precede consumers; visual mockups before UI |
| 3 | Remote editor launch/browser IDE; second-host recovery drill; per-bench isolation; Salaam layout and execution-host integration | Pending |

For each checkpoint: run required checks, commit in this repository, verify remote
PR state before pushing, and record concrete verification and remaining limits.
Production tags remain operator-only. Cleanup requires a reviewed dry-run list;
keep that approval distinct from permission to implement the product.

## Published checkpoints

The first sixteen implementation PRs are merged into `main`. The entries below
record their original verification and dependency order; references to stacked
PRs describe their publication history.

- `c874bf6`: initial CLI, journaled clean-worktree adoption, failure experiments
  and full phase checklist. Local checks and Forge push CI passed.
- `ea47c2f`: versioned SQLite state, legacy import, rollback and read-only status;
  local race tests, Linux failure tests and Forge CI passed; PR #1 is merged.
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
  drills passed. Automatic acquisition hooks remained pending at that checkpoint;
  see [setup](setup.md) for the subsequent implementation.

- Automatic acquisition preparation: setup-to-command/cooperative handoff holds
  one lease, protects all stage logs and revokes continuation after a successful
  stop. Local/Linux required checks and actual daemon/cgroup failure drills
  passed; creation warm-up and remaining Phase 1 scope stay pending.

- Acquisition mirror refresh: normal lease/run/setup validates the registered
  checkout/mirror and refreshes remote-tracking branches while preserving local
  work. Mirror operations have a configurable deadline; failed refresh grants
  no ordinary acquisition, and local repair stays network-independent.

- Initial creation/recovery preparation: registration reserves ownership before
  setup, success checkpoints/releases, and failure retains evidence and its lease.
  Actual Linux canonical cases, CLI/old-service compatibility and interrupted
  request replay protection passed; see [creation](creation.md).

- Archive grace: a persisted seven-day default deadline binds reviewed v2 plans;
  early reap and caller-shortened deadlines are refused before effects. Schema-8
  SIGKILL intent recovery and managed XFS retirement regressions passed. Read-only
  enforcement is verified in the following checkpoint; automatic completion archive
  remains pending.

- Read-only archives: a durable exact-tree permission intent precedes mode changes;
  pending transitions block acquisition/planning and resume only known changes.
  Reviewed v3 deletion reopens only approved directories. Linux/macOS race checks,
  real SIGKILL/restart, unprivileged write refusal and XFS retirement passed; see
  [archive permissions](archive-permissions-output.txt).

- Repository cache settings: typed sharing flags select fixed shared or
  checkout-local pnpm/npm/Bun/Playwright/Go paths across setup and supervised
  commands. Actual pnpm, daemon handoff, once invalidation, unprivileged service
  and XFS private-cache cap checks passed; see [caches](caches.md).

- Repository quota requests: creation pins a byte request from its source commit;
  managed hosts enforce it within the root ceiling without resizing existing
  allocations. State schema 11 and ledger format 2 preserve old allocations and
  refuse old binaries after upgrade. Request drift blocks new work while repair,
  checkpoint and release remain available. See [quota requests](quotas.md) for
  compatibility and verification.

- Repository archive policy: explicit `branch-merged` policy and recorded
  unmerged-to-merged checkpoint evidence gate the same durable read-only archive
  transition. A bounded daemon loop and local sweep command inspect candidates;
  missing history, uncertain merge state and held/dirty work remain preserved.
  Reviewed reap is still separate. See [automatic archive](automatic-archive.md).

- Multi-repository checkpoint foundation: a pinned complete-vector manifest,
  durable generation journal, member tag pins and a final remote metadata seal
  preserve recovery selection across partial pushes. Group creation, execution,
  lease, release and archive integration remain pending; see
  [checkpoint generations](checkpoint-generations.md).

- Git control handoff: managed Git commands use durable cgroup identities and
  recovery before new ownership. Command-local policy suppresses client hooks
  after payload fencing, while user work can run hooks inside its payload.
  Schema 13 prevents older binaries from ignoring live control intents; see
  [Git control operations](git-control.md).

- Aggregate admission foundation: the strict Go v2 request parser checks the
  mandatory schema/corpus pinned to treaty `04813138dd1dc32aef2e528fc08dd7e76cca92f5`
  (merged in treaty #33). It validates request shape only; the service still
  exposes v1. A separate workspace planner selects every member's immutable
  source and quota declaration before any task checkout is created. Matching
  declarations select one total allocation; conflicting declarations refuse
  rather than summing member budgets. A failed later source returns no partial
  plan. Mirror preparation and source observation share a bounded member context.
  SQLite file opening is extracted without changing locking or migration policy.
  Durable aggregate creation, quota allocation, setup, leases, checkpoints,
  recovery, archive and v2 service/CLI responses remain to be integrated.
  Native `sh scripts/check.sh` passed, including vet, race tests, installer
  tests and all three builds. Two-repository Git tests cover source selection,
  a failing second source, total-budget conflicts and no checkout effects.

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


## Phase 1 acceptance still to prove

The original proposal remains the acceptance source; the checkpoints above do
not replace its scope. Before calling Phase 1 complete, explicitly verify the
standalone laptop preparation path (supervised hooks currently require Linux),
multiple repositories within a bench, automatic completion/archive
lifecycle and broader collection, and the
AgentPod acquisition/prompt/checkpoint pilot on an opt-in board. The review of
charter/treaty boundaries must precede suite consumers. Source-level progress or
passing single-host drills alone is not evidence that those outcomes are done.
