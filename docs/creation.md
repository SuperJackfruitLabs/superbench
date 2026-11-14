# Initial checkout preparation

This single-host checkpoint is verified on Linux; it does not complete Phase 1.
The contract remains a proposal pending review.

`bench create` and `bench recover` register the pinned checkout and prepare it
before returning success. Repository once/each hooks take precedence over frozen
pnpm/npm setup. A repository with neither hooks nor a supported lockfile needs no
setup process. Supervised preparation currently requires Linux cgroup delegation;
the standalone laptop preparation path remains unfinished.

When preparation is needed, registration reserves an initialization lease in the
same SQLite transaction. Another acquisition cannot enter between registration
and setup. The setup stage uses that lease, fences descendants and seals output,
then checkpoints and releases only on success with clean, remotely confirmed work.
Success means the checkout is ready for a separate acquisition; it grants no
writable lease to the caller.

Failure preserves the checkout, logs and lease. The creation response includes
the held token for the requesting operator; status still redacts it. Inspect the
output and files, fence an interrupted execution, and explicitly checkpoint and
release after reconciliation. No unknown files are committed or deleted. Repeating
creation with a new request ID does not rerun setup on a registered key.

`create-cold` and `recover-cold` are explicit local administrative commands that
register the checkout without running setup. They are useful for maintenance and
for fixtures that test setup independently. They are unavailable as named socket
operations and are never an automatic fallback from failed warm creation.

## API compatibility

The proposed `superbench.v1` request adds optional `initialize: true` to create and
recover. The current CLI sends it explicitly. An older service rejects this
unknown field before effects; the CLI must not retry without it. Omission retains
legacy cold-registration semantics for old clients. False, null, wrong types,
unknown bypass flags and initialization on other operations are rejected.

The existing per-UID durable request receipt covers the full registration/setup/
checkpoint/release operation. A completed retry returns the recorded outcome.
After service interruption, the same request becomes `operation_in_doubt` and
must not redispatch initialization, even after the operator has reconciled and
released its checkout. Receipts remain replay protection and are not collected.

## Verification

`scripts/test-creation.py` executes the canonical creation lifecycle scenarios
against a real Linux daemon and Git repositories. Its only request substitution
is the abstract repository URL, bound to a disposable local remote. It checks
success/failure, retained ownership, remote checkpoints, once receipts, preserved
dirty files and stable retries. It also checks old-service rejection, legacy cold
omission, pre-launch configuration/lockfile errors, actual CLI defaults and daemon
interruption. Run with the new CLI/service and the published
pre-initialize service; root and a disposable cgroup-v2 host are required.

The lifecycle, compatibility, CLI, unprivileged-service and crash drills passed
on a disposable Linux host. See [recorded results](creation-linux-output.txt).
The canonical data is pinned to treaty `e66ec6098980ca09472440bc6d5aebf241f43a35`.
Schema tests validate request vectors separately from these runtime assertions.
