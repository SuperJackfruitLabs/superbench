# SuperBench single-host prototype

A bench is one task's git worktree, kept warm between cooperating agents. This is
an experimental CLI and local Unix-socket daemon, not a production release. It
has no TCP listener or dependency on another suite product.

## Build and use

Requires Go 1.26.4 and git on Linux or macOS. Run `go build -o bin/bench ./cmd/bench`.
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
caps, fixed-policy root allocation, free-space admission and bounded run logs.
`bench setup` supports frozen pnpm/npm installs. See [storage management](docs/storage.md)
for provisioning prerequisites, budgets, compatibility and verification.

## Supervised runs

Linux hosts with a delegated cgroup-v2 root can use `bench run` and `bench stop`
for supervised process-tree fencing. Success checkpoints and releases; failures
retain the lease. See [execution and recovery](docs/execution.md) for provisioning,
log output, schema upgrades, local administration and the isolation boundary.

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
If create/recover stops after creating its worktree but before registration, retry
with exactly the same arguments. The intent pins the original SHA; adoption
requires the expected path, shared git directory, branch, HEAD and clean files.
Conflicts are preserved for manual review. Intents are retained as creation records.
Partial mirror clones are staged before publication; abandoned clone directories
remain bounded for operator review. Filesystem and SQLite changes are not one
atomic transaction.

## Safety boundaries and deliberate limitations

- One host, one repository per bench. No cross-host lease coordination. Recovery
  elsewhere requires the operator to stop the old host's writer first.
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
- Git/network failures may block; there are no service timeouts yet. Git and state
  updates are not one transaction: journaled worktree creation can be resumed,
  while conflicting files require review and a pushed checkpoint may need retrying
  if saving state failed.
  No automatic deletion/reset is attempted.
- Archive/retention, remote authentication, UI and suite
  integrations are not implemented. The local API authenticates Unix peer uids.
- Only trusted repositories are suitable. Local git configuration and hooks apply.
  Verification should use a separate clean checkout of the pushed checkpoint.

Storage feasibility evidence is in sketchbook's `ideas/2026-11-14-superbench/research/`.

## Development

Run `sh scripts/check.sh`: formatting, zero-warning vet ratchet, architecture guards,
race-enabled integration tests and build. See [code quality](docs/code-quality.md).
SQLite is provided by the pinned pure-Go modernc.org/sqlite driver. Forge is the primary remote; the first three implementation checkpoints passed
Forge CI. No production deployment is configured.
