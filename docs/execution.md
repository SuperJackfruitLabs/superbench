# Supervised execution

`bench run [flags] KEY -- COMMAND [ARGS]` acquires a durable lease and runs one
noninteractive command in the task checkout. Output goes to a private run log in
state storage; the JSON result gives its path, command status and retained lease
when recovery is needed. The holder token is removed from the command environment.

On Linux the runner requires a writable, delegated cgroup-v2 subtree, a stable
machine identity, clone3 with CLONE_INTO_CGROUP, and cgroup.kill. Pass its path via
`--cgroup-root`. Provision/delegation belongs to the host service installer; the
CLI does not create a delegation under the system root automatically.

```sh
bench run --holder coding-agent --cgroup-root /delegated/cgroup fix-login -- codex exec 'Fix login'
bench run --holder reviewer --checkpoint=false --cgroup-root /delegated/cgroup fix-login -- sh ./verify.sh
```

Before launch, state records a random scope ID, stable host hash, boot identity,
starting phase and holder token. Start and stop take the same per-bench execution
lock. The command is born inside its scope rather than moved there afterward.
Stop marks the execution stopped only after the kernel reports no remaining
scope members and the empty scope can be removed. Detached setsid descendants
remain in the cgroup. A delayed launcher checks the durable phase inside the
execution lock and refuses to start after a completed stop.

A successful command is fenced, then its existing committed HEAD is checkpointed
and the lease released. There is no implicit git add or commit. Dirty files,
failed commands, rejected pushes and ambiguous saves leave the lease held.
`--checkpoint=false` retains a stopped lease even on success. Normal nonzero
command exit statuses propagate through the CLI when fencing completed.

## Recovery and local administration

```sh
bench stop --token "$BENCH_LEASE_TOKEN" fix-login
bench stop --admin fix-login
```

`--admin` is an explicit local administrative operation for someone with access
to private state. A successful administrative stop returns the retained lease
and token so the operator can inspect, checkpoint and release it. Network callers
must never obtain this authority merely by supplying a boolean request field.
The local service enforces operator authorization from kernel Unix peer
credentials; see [the service API](service.md).

Killing the supervisor does not free the lease. The cgroup stays recorded so
another local process can fence the surviving tree. Cancellation kills the known
scope before attempting a state update, so a full state filesystem does not keep
that tree alive. A failed state save still requires reconciliation; it never
implies release. A different host is refused, while a new boot of the same host
proves that its old processes cannot still be running. Copying state between
hosts is not a supported handoff; use confirmed-branch recovery and stop the old
host's writer first.

## Compatibility and limits

State schema 2 protects execution metadata: the schema-1 SQLite binary refuses
it rather than silently dropping execution fields. `bench migrate` explicitly
upgrades schema 1; the first successful mutating operation also upgrades it.
Read-only status never migrates. Stop older JSON-prototype processes before an
upgrade; concurrent old JSON and SQLite writers are unsupported.

On macOS and unsupported Linux hosts, fenced run refuses to start before taking
a lease. The cooperative lease/checkpoint CLI remains available. There is no
silent process-group fallback. No per-bench uid, mount or network isolation is
implemented yet. Commands still have the launching user's authority, so deliberate
cgroup escape by that user is outside this boundary. Phase 3 supplies stronger
isolation; this is lifecycle fencing for trusted commands, not a sandbox.

### Project quotas and the payload account

XFS project quotas alone do not hold against a file's owner. From the initial
user namespace, the owner can change the project ID of its own files
(`xfs_io -c 'chproj 0' FILE`) and clear the project-inherit flag on its own
directories, with no capability. Writes to those files or directories then count
against another project, outside the bench's byte and inode caps. A Fresh bench's
tree belongs to the payload account, so SuperBench confines that account.

- **Prevention.** `bench-payload` installs a seccomp filter after `no_new_privs`
  and the credential drop, before the payload runs anything. Every thread of that
  process gets it, and so do all its children. The filter covers setup and run
  stages, member and root runs, and every Git route through the helper. It refuses
  `ioctl` `FS_IOC_FSSETXATTR`, `FS_IOC_SETFLAGS` and `FS_IOC32_SETFLAGS` (it compares
  the request's low 32 bits, which is what the kernel uses), as well as
  `file_setattr`, `io_uring_setup`, `io_uring_enter` and `io_uring_register`. Each
  refusal fails with `EPERM`. Only amd64 and arm64 are supported. Any other
  architecture, the 32-bit compat ABIs and x32 are killed. On other CPU
  architectures the helper refuses to launch. Reading attributes still works. The
  helper checks that the kernel reports seccomp filter mode before it executes the
  payload.
- **Detection.** Root's read-only `bench-quota verify bench KEY` checks that the
  bench's leaf directory carries the bench's project ID and the project-inherit
  flag. It reads them through a descriptor and never follows a symlink. If either
  check fails, it refuses with `bench leaf is outside its quota project`. benchd
  runs this verify before each Fresh launch and lease, and before it exports a
  checkpoint from a managed Fresh bench. An absent leaf holds no data and passes.
  The leaf check is cheap. Walking the whole tree on every launch is not, so a
  full-tree check is provided for reclaim; no released-bench reclaim calls it yet.
  It runs as root through descriptors only, never follows a symlink and never
  leaves the filesystem. It is bounded by the bench's inode cap, and every
  directory and regular file must carry the project, with directories also
  carrying the inherit flag. Symlinks and special files are counted but not
  opened.

The filter does not cover a process that runs as the payload account without
going through `bench-payload`. Keep that account without a login shell,
`systemd --user` lingering, cron or at jobs.

Storage budgets and the [delegated Linux service installer](install.md) are
implemented. Automatic log retention is still pending. Interactive terminal
attachment belongs to Phase 2.

## Verification

Local race-enabled tests cover checkpoint ordering, retained failures, blocked
release before fencing, stop authorization, start failure, stripped lease-token
environment and a stopped intent refusing a delayed launch. Real Linux tests in
scripts/test-execution.sh and scripts/test-execution.py cover detached descendants,
supervisor SIGKILL, wrong-token stop, administrative recovery, cancellation, full
state, schema refusal by the previous binary and foreign-host metadata refusal.
Run the shell script inside `unshare --mount` as root on a disposable host, passing
absolute paths to the Linux CLI, Python test and optionally the old SQLite CLI.
The test makes only a temporary cgroup subtree and an 8 MiB private state tmpfs.
