# Changelog

Changes people will notice, newest first. Add an entry under `## Unreleased` with each such
change; before a tag, a release-prep PR runs `sh .github/release/prep-release.sh vX.Y.Z`, which
stamps the section `## vX.Y.Z — YYYY-MM-DD`, and the release notes quote it.

## Unreleased

## v0.0.2 — 2026-11-23

- The docs explain workspaces whose repositories a card chooses upstream ("Repositories chosen per
  card" under Benches and workspaces): SuperBench creates a workspace with the 1 to 32 members it is
  given, never changes an existing workspace to another set, and a hub that reclaims a settled
  card's workspace needs the node's user in `--operator-uids`.

- A checkpoint or release refused for a dirty tree now names what to clean. The `409 conflict`
  message ends with the first five offending paths and a count of the rest, for example
  "workspace has uncommitted or untracked files; commit or remove them before checkpoint or
  release: src/__pycache__/app.cpython-312.pyc, .cache and 2 more" (v1: "bench has …"). Paths
  are relative to the workspace root, prefixed with the member name when the workspace has
  several; ignored files are not named and no file content is sent. The code and status are
  unchanged, and the message stays within 256 bytes. A stray top-level entry in a Fresh
  workspace is still refused without a path.

## v0.0.1 — 2026-11-22

First release. SuperBench is an experimental single-host prototype, not a production system, and
nothing here has been proven beyond what the repository's docs describe. A bench is one task's git
worktree, kept warm between cooperating agents: the `bench` CLI and the local Unix-socket daemon
`benchd` create it, hand it from one agent to the next under a cooperative lease, checkpoint its
commits to the remote, and recover it into fresh host state. On Linux, managed storage adds
per-bench byte and inode caps on a dedicated XFS mount, supervised runs in cgroups, and the
default-off v2 workspace API, whose Fresh layout keeps each member's tree private to a separate
payload account that benchd reaches only through the payload helper.

The release carries the four commands the managed installer needs: `bench`, `benchd`,
`bench-quota` and `bench-payload`, in one archive built for Linux amd64 with CGO off, so every
binary is static (`bench-payload` must be). The archive is reproducible from the tag, and
`SHA256SUMS` and a build-provenance attestation come with it. `bench version` and
`benchd version` print the release version; a build from a branch prints `dev`. Read
`docs/pilot-installation.md` before installing anything.

- A ready workspace whose owner was stopped with stray files in its tree could not be freed: the
  checkpoint refused the files, and a stopped owner can never launch the run that removes them. New
  root-only `bench workspace renew_admin KEY` retires the stopped token and prints a new one
  (`grant_token`) for the same holder, with fresh launch authority. The old token stays stopped and
  is refused. It requires real and effective UID 0, a stopped owner whose run is stopped, no
  archive, transfer, initialization repair or pending checkpoint, and no active daemon operation on
  the key; every refusal is a fixed message. It is local only, not a socket operation. The operator
  path for a dirty stopped lease is: `stop_admin`, then `renew_admin`, then
  `BENCH_LEASE_TOKEN=NEW_TOKEN bench workspace run --holder HOLDER KEY -- COMMAND`, where the command
  commits or removes the files and the run checkpoints. Then `release` if the lease is still held,
  and optionally `reclaim`.
- A checkpoint or release refused because the tree has uncommitted or untracked files now says
  so: `409 conflict` with "workspace has uncommitted or untracked files; commit or remove them
  before checkpoint or release" (v1: "bench has …"), instead of the generic "workspace state
  conflicts with this operation". The code and status are unchanged and the message stays static:
  it names no path or file.
- A Fresh create whose source commit the mirror held only under `refs/remotes/origin/*` failed
  with `bench-quota: exit status 128` and stayed at `materializing`. That was any branch already
  pushed past `main`: the next generation of a reclaimed key whose bench had pushed work, or a
  create on a card branch with commits. Each member's clone now fetches exactly the source
  commit from the daemon's mirror before the checkout; nothing is fetched from the network, no
  other branch is copied into the bench and the mirror is not written. A bench already stuck at
  `materializing` from this still needs the recovery steps in `docs/fresh-bootstrap-recovery.md`.
- A Fresh create whose initialization stalled after its bootstrap completed (the workspace stays
  `initializing`) no longer holds its key for good either. Once its initialization owner is
  stopped (`stop` or `stop_admin`), a `create` on the key with a new request ID has the helper
  reclaim the bench, with root's own in-use, containment and leaf checks, and makes the key's
  next generation. A failed create's recovery approvals are now named per generation
  (`KEY@N`), so a key whose create fails a second time is recovered the same way.
- A Fresh create that failed no longer holds its key for good. Once root has recovered it
  (denied its operation with `bench-quota cancel-operation`, cleaned, removed the empty leaf and
  retired the allocation; this covers a create still `materializing` with no quota project), a
  `create` on the key with a new request ID asks the helper's
  read-only `inspect-retired-payload` and, on root's proof, makes the key's next generation with
  a new `bench_id` and a new quota project. Without that proof it is a conflict, as before. The
  recovery docs now say how to deny the original operation.
- benchd reclaims a released Fresh bench by itself once it has been idle longer than
  `fresh_reclaim_idle_hours` in `/etc/superbench/activation.json` (default 24 hours while
  `fresh_payload` is on; `0` turns it off). Its sweep runs the same reclaim as
  `bench workspace reclaim`, and a reclaim that fails is resumed by the next sweep. Idle time
  starts when the last writer releases; a bench released before the upgrade starts its grace
  at the first sweep.
- A `create` on a reclaimed key makes a new bench: the key's next generation, cloned again from
  its branches, with a new `bench_id`, a new quota project and a fresh tree. The old project is
  never reused and the old evidence is kept. The quota ledger becomes format 4 the first time a
  key is created again; older binaries refuse to read it.

- A released Fresh bench can be reclaimed: `bench workspace reclaim KEY` (v2 operation
  `reclaim`) deletes its payload-private tree as the payload account, removes the empty leaf
  and retires its quota project, so a later create can use the space. Before, every Fresh
  create kept its reservation for good and a host filled up after a few cards. The bench ends
  `reclaimed`; `reclaiming` never returns to `ready`, and after any failure the same command
  continues. A legacy workspace is refused and keeps archive and reap.
- A Fresh workspace now runs its whole lifecycle on an installed service. `bench workspace
  create` stopped at `initializing` with `registered payload allocation requires its retained
  bootstrap authority`, because setup, runs and lease reapplied the bench quota through the
  ordinary allocation that the helper refuses for a Fresh bench. They now check its pinned
  allocation read-only with `bench-quota verify bench KEY`.
- A Fresh create past the volume's reservations now answers `422 capacity_exhausted` with the
  logical quantities, as a non-Fresh create does, instead of `operation_failed`.
- `install.py --upgrade --previous SHA256SUMS --sums SHA256SUMS --backup DIR` replaces the
  four installed executables with a newer revision's, while the unit is stopped.
  - Each executable must be the previous reviewed build, as a plain root-owned file. It is
    backed up to the new directory and then renamed atomically over the old one.
  - A foreign or changed executable, or any differing configuration file, refuses before any
    effect.

  A plain rerun still refuses differing files. "Reinitialize the indexed ledger" now uses this
  mode, unmounts before `mkfs.xfs` and reruns `configure-host`.
- A Fresh workspace now initializes. With `fresh_payload` on, `bench workspace create` used to
  stop at `initializing` with `lstat …/benches/<key>/<member>: permission denied`, because
  benchd read the payload-private member tree itself. Now it reaches that tree only through the
  payload helper, as the payload account: inspection, preparation inputs, setup, runs,
  checkpoint and release. A checkpoint imports new commits into the daemon's mirror as a
  bounded Git bundle exported by the payload, checked strictly in a quarantine before any
  object reaches the mirror. Some steps refuse a Fresh workspace before
  touching it:
  - archive, transfer and cross-host export;
  - setup from a lockfile, or with explicit cache settings.

  Workspaces in the legacy layout are unchanged.
- Durable storage records survive a reboot that renumbers a device. The quota ledger's ready
  record and authority identify the state filesystem by its UUID (plus the subvolume on btrfs)
  instead of its device number, and initialization refuses a state filesystem without one.
  Transfer approvals, storage retirements, the root checks of missing-source freezes and Fresh
  recovery compare a managed data volume's device only within one boot; the volume itself is
  bound by its UUID, so a loop image reattached at another minor no longer makes Fresh recovery
  refuse. Archive reap trees, run history, payload layouts, the payload bootstrap intent, helper
  operation identities, worktree transfer evidence and mirror stages still pin device numbers
  and refuse after a renumbering; "Reboot and device numbers" in `docs/pilot-installation.md`
  lists them and the host setup that avoids it. A ledger initialized by the previous revision
  is refused with a pointer to "Reinitialize the indexed ledger". Unmanaged storage, which has
  no UUID, still refuses a renumbered device, now with a message that says so.
- The quota helper's storage authority no longer hashes the data mount's mount ID or device
  number. A ledger that `install.py --storage` initializes in the host's mount namespace now
  opens in the service unit's private namespace and after a reboot; before, every start with
  `fresh_payload` on failed with `quota operation journal authority changed`. A ledger
  initialized by an earlier revision is refused with a message that points to "Reinitialize the
  indexed ledger" in `docs/pilot-installation.md`. The data volume must not be mounted with
  `nouuid`.
- benchd reads the default-off v2 switches (Fresh payload layout, finite I/O, receipt lookup,
  cancellation, registration observation) only from the root-owned
  `/etc/superbench/activation.json`, and logs them at start. With the file absent nothing
  changes. An activated daemon refuses state that a start without the switches has not migrated,
  and refuses a combination that would strand workspaces: `fresh_payload` needs `cancellation`
  and the installed service identity, and `finite_io` and `registration_observation` need
  `fresh_payload`.
  The managed installer creates the separate `superbench-payload` account and, with
  `--identity`, writes the payload policy and service identity the helper verifies; with
  `--activation` it writes the activation file with every switch off.
- Return a specific v2 logical-capacity refusal with needed and total byte/inode budgets when a fixed quota reservation cannot fit. Preserve quota limits, retained creation intents and nonzero failures; log only the same bounded diagnostics. Keep the v1 closed error code and message unchanged.

- Add a proposed explicit finite-I/O request codec with bounded opaque bytes and
  immutable digest vectors; keep legacy request encodings and service entrypoints unchanged.

- Add fixed-policy XFS allocation, host admission and shared store caps, frozen
  pnpm/npm setup, atomic mirror publication and bounded/reserved run logs.

- Add Linux cgroup-backed run/stop commands, retained leases on supervisor failure,
  local administrative fencing and schema-2 protection for execution metadata.

- Replace provisional JSON state with versioned SQLite, one-time legacy import,
  rollback-safe lease updates and read-only status.

- Persist creation intent before worktree changes; retry safely registers an exact
  journaled checkout after interruption and refuses dirty or conflicting files.

- Add a local experimental CLI for task worktrees, persistent cooperative leases,
  remote-confirmed checkpoints, warm handoffs and recovery into fresh host state.
