# SuperBench single-host prototype

A bench is one task's git worktree, kept warm between cooperating agents. This is
an experimental CLI and local Unix-socket daemon, not a production release. It
has no TCP listener or dependency on another suite product.

## Build and use

Requires Go 1.26.4 and git on Linux or macOS. Run `go build -o bin/bench ./cmd/bench`.

For explicit cooperative laptop setup, see [local preparation](docs/cooperative-local-preparation.md).
Flags precede the task key. Output is JSON. Use absolute paths for local git remotes.

```sh
bin/bench create --repo /absolute/path/to/remote.git fix-login
bin/bench lease --holder first-agent fix-login
# Save Lease.Token from the response in BENCH_LEASE_TOKEN and cd to Path.
# Edit files, git add, and git commit as usual. Then:
bin/bench checkpoint fix-login
bin/bench release fix-login
bin/bench lease --holder second-agent fix-login
bin/bench status fix-login
```

Creation and recovery run initial setup before returning readiness; see
[initial checkout preparation](docs/creation.md) for ownership, failure recovery,
API compatibility and Linux verification.

`checkpoint` pushes the existing HEAD to `bench/fix-login` and confirms the remote
has exactly that commit. It never invents a commit or force-pushes. `release`
requires a clean worktree, current HEAD checkpointed, and the remote still matching.
Untracked files block checkpoint and release; ignored build files do not.
A rejected push leaves the lease held. Lease tokens are printed only on acquisition;
status/checkpoint redact them. Treat saved state and acquisition output as private.
Prefer the environment variable to `--token`, which exposes tokens in process arguments.

`--state /absolute/state` and `--data /absolute/data` are available on every command.
Defaults use the OS config directory for state and cache directory for bench data.
They cannot overlap, including via symlinks. Use the same paths on every invocation.
Mirrors share git objects across benches; fetches update remote-tracking refs, never
branches another bench has checked out. Do not manually prune mirrors or alter refs.

## Local daemon

Build `go build -o bin/benchd ./cmd/benchd`, then run it with private state and an
absolute socket path. Use `bench <operation> --socket /absolute/api.sock ...` to
connect. Linux supervised execution requires a delegated cgroup root. See
[the service API](docs/service.md) for permissions, stable request IDs, restart
reconciliation and operational limits.

For Linux installation, stage and review the [systemd payload](docs/install.md).
Its installer prepares files and accounts without starting the service.

## Managed storage

Linux hosts can configure a dedicated XFS data mount with per-bench and shared
byte/inode caps, fixed-policy root allocation, free-space admission and bounded run logs.
`bench setup` supports frozen pnpm/npm installs and explicit repository
[once/each setup hooks](docs/setup.md), automatically honored before runs and
cooperative lease handoff. Repository [cache settings](docs/caches.md) select
shared or checkout-local stores for managed commands. Repository
[quota requests](docs/quotas.md) select smaller allocations within root policy.
See [storage management](docs/storage.md)
for provisioning prerequisites, budgets, compatibility and verification. Existing
byte-only hosts require the [explicit inode migration](docs/inodes.md).

Ordinary acquisition also [refreshes the shared mirror](docs/mirrors.md),
preserving the task branch and all local work. Remote failure refuses acquisition;
the local repair lease remains available for maintenance.

## Supervised runs

Linux hosts with a delegated cgroup-v2 root can use `bench run` and `bench stop`
for supervised process-tree fencing. Success checkpoints and releases; failures
retain the lease. See [execution and recovery](docs/execution.md) for provisioning,
log output, schema upgrades, local administration and the isolation boundary.
Managed [Git control scopes](docs/git-control.md) also fence checkpoint helpers
before ownership is released and recover their durable intent after a crash.

## Archive and reap

A released, clean, remote-confirmed bench can be archived as a read-only tree. Deletion requires its
persisted grace period to have elapsed and explicit approval of the complete file
manifest. The default grace period is seven days. See
[archive and reap](docs/archive.md) for retained artifacts, retry rules and managed
quota retirement. Archive/reap are local administrative commands in this checkpoint.
Committed [repository archive policy](docs/automatic-archive.md) can opt into
automatic read-only archival after a verified unmerged-to-merged checkpoint
transition. Deletion still requires an exact reviewed plan after the grace period.

## History and log retention

Supervised runs retain private history and sealed output evidence. Use reviewed
[log collection](docs/retention.md) to free released output; held, open or unknown
logs remain protected. Collection can resume after a crash without rerunning work.
The daemon can also run an explicitly approved [automatic retention
policy](docs/automatic-retention.md), with bounded batches and durable recovery.
Automatic retention is disabled until the operator reviews and enables a policy.

## Recovery

On a replacement host with empty state/data directories:

```sh
bin/bench recover --repo /absolute/path/to/remote.git --branch bench/fix-login fix-login
```

Only the remote-confirmed committed files recover. Ignored build products,
untracked files and unpushed commits do not. Recovery requires the remote task
branch to exist. The implementation does not reconstruct a lost checkout in an existing
state store or reset a conflicting local branch. An interrupted unregistered
creation with a valid intent can now be resumed by repeating its original command. Keep the old files/state intact
and investigate; use a separate replacement state/data pair after stopping old writers.

## Implementation status

Phase 1 has started with durable creation intents. See [the implementation sequence](docs/implementation.md).
The grouped workspace API and CLI are described in
[workspace API integration](docs/workspace-api.md), including verification limits
and the remaining standalone preparation and pilot work. Local reviewed
[interrupted-history reconciliation](docs/history-reconciliation.md) records an
unknown outcome without resuming a command or fabricating setup success.
Local reviewed [initialization repair](docs/initialization-repair.md) adds an
explicit fresh epoch for stopped grouped setup while preserving its evidence.
Reviewed [original task-ref transfer](docs/workspace-transfer.md) permanently
relinquishes a source before warming a fresh local destination on its branches.
Reviewed [missing-checkout recovery](docs/workspace-missing-transfer.md) retains
surviving files and administrative evidence and rebuilds only the last complete
confirmed checkpoint. Native/Linux checks and managed recovery regressions
[passed](docs/workspace-missing-transfer-output.txt). Separate reviewed
[source archive/reap](docs/workspace-missing-archive.md) preserves missing-member
evidence and requires zero actual quota usage before retirement.
If create/recover stops after creating its worktree but before registration, retry
with exactly the same arguments. The intent pins the original SHA; adoption
requires the expected path, shared git directory, branch, HEAD and clean files.
Conflicts are preserved for manual review. Intents are retained as creation records.
Partial mirror clones have durable ownership journals before publication.
[Reviewed staging collection](docs/mirror-clones.md) requires original managed
Linux writer fencing; cooperative and legacy abandoned directories remain protected.
Native/Linux checks and the managed crash drill [passed](docs/mirror-clones-output.txt). Filesystem and SQLite changes are not one
atomic transaction.

## Safety boundaries and deliberate limitations

- Single-repository benches and grouped workspaces use local ownership. Reviewed
  [cross-host administrative handoff](docs/cross-host-handoff.md) retains original
  task refs through explicit source retirement and destination import. It adds no
  network lease coordination or suite API; the operator must independently verify
  the old host's writer stopped before approving import.
- Leases are cooperative: they block other CLI acquisitions, not direct edits,
  arbitrary shells, or processes sharing the same uid. Supervised Linux runs use
  cgroup fencing; neither mode provides uid/mount isolation.
- No automatic expiry. Fenced executions can be stopped by their holder or the
  local administrator; cooperative shell leases still need manual inspection. A process crash preserves the lease. Stop and
  inspect the holder before using its saved token to checkpoint/release. If a supervised holder token is lost, local `bench stop --admin` fences the
  recorded scope and returns the retained lease. No process is reassigned by time.
- State is versioned SQLite outside bench data, with FULL synchronous commits.
  A host-wide OS file lock also serializes git/filesystem operations. Existing
  prototype JSON is imported once on the next successful write and preserved as
  a historical snapshot; SQLite is authoritative thereafter. Status is read-only.
  All commands serialize, including git calls. Use local filesystems only.
- Git/network calls may still block without an application deadline; transport
  deadlines do not prove a core operation stopped. Git and state
  updates are not one transaction: journaled worktree creation can be resumed,
  while conflicting files require review and a pushed checkpoint may need retrying
  if saving state failed.
  No automatic deletion/reset is attempted.
- Shared-cache/journal collection, remote
  authentication, UI and suite
  integrations are not implemented. The local API authenticates Unix peer uids.
- Only trusted repositories are suitable. Local git configuration and hooks apply.
  Verification should use a separate clean checkout of the pushed checkpoint.

Storage feasibility evidence is in sketchbook's `ideas/2026-11-14-superbench/research/`.

## Development

Run `sh scripts/check.sh`: formatting, zero-warning vet ratchet, architecture guards,
race-enabled integration tests and build. See [code quality](docs/code-quality.md).
SQLite is provided by the pinned pure-Go modernc.org/sqlite driver. Forge is the primary remote; the first three implementation checkpoints passed
Forge CI. No production deployment is configured.
