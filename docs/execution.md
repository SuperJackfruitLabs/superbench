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
