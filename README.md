# SuperBench local CLI prototype

A bench is one task's git worktree, kept warm between cooperating agents. This is
an experimental local prototype, not benchd or a production release. It has no
network API or dependency on any other suite product.

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
This does not yet cover partially cloned mirrors or power-loss durability across
filesystems.

## Safety boundaries and deliberate limitations

- One host, one repository per bench. No cross-host lease coordination. Recovery
  elsewhere requires the operator to stop the old host's writer first.
- Leases are cooperative: they block other CLI acquisitions, not direct edits,
  arbitrary shells, or processes sharing the same uid. No sandbox/fencing yet.
- No automatic expiry or takeover. A process crash preserves the lease. Stop and
  inspect the holder before using its saved token to checkpoint/release. If the
  token is lost, this prototype has no safe automated takeover command.
- State is an atomic JSON file with an OS file lock, file fsync and directory
  fsync, outside bench data. This is a prototype substitute for proposed SQLite.
  All commands serialize, including git calls. Use local filesystems only.
- Git/network failures may block; there are no service timeouts yet. Git and state
  updates are not one transaction: journaled worktree creation can be resumed,
  while conflicting files require review and a pushed checkpoint may need retrying
  if saving state failed.
  No automatic deletion/reset is attempted.
- Quotas, package setup, archive/retention, host admission checks, service APIs,
  authentication, UI and suite integrations are not implemented.
- Only trusted repositories are suitable. Local git configuration and hooks apply.
  Verification should use a separate clean checkout of the pushed checkpoint.

Storage feasibility evidence is in sketchbook's `ideas/2026-11-14-superbench/research/`.

## Development

Run `sh scripts/check.sh`: formatting, zero-warning vet ratchet, architecture guards,
race-enabled integration tests and build. See [code quality](docs/code-quality.md).
No external Go dependencies. Forge CI configuration is prepared but has not run on
a remote. Forge is the primary remote. Initial implementation checkpoints are published there; no production deployment is configured.
