# Reviewed initialization repair

A stopped grouped create/recover retains its workspace, allocation, files and
evidence. Replaying its original request cannot restart setup or obtain a new
repair capability. Local administration can explicitly inspect, approve and
continue a fresh initialization epoch without replacing the workspace identity.

```sh
bench workspace stop_admin --state /private/state --data /workspace/data card
# Reconcile an interrupted execution's unknown history first, when required.
bench workspace inspect-initialization --state /private/state --data /workspace/data \
  card > initialization-plan.json
# Review the complete plan and copy its Digest; choose a fresh repair ID.
bench workspace claim-initialization --state /private/state --data /workspace/data \
  --plan initialization-plan.json --approve DIGEST --repair-id repair-1 card
# Use the grant_token returned by the approved claim.
bench workspace continue-initialization --state /private/state --data /workspace/data \
  --repair-id repair-1 --token TOKEN card
```

Use the configured storage and cgroup-root settings. These commands require
direct local state authority and refuse remote socket options. Plans and results
are bounded closed JSON. Protect the claim result as a capability. Continuation
requires an explicitly supplied token; it does not read an ambient lease token.

Inspection requires an explicitly stopped initialization owner, revoked private
continuation and independently empty recorded execution scope. Its plan binds
the exact private row digest, workspace identity, current complete HEAD vector,
pending generation, setup-input and once-receipt digests, retained history and
locked capture observations. A changed row, checkout, receipt, capture or fence
requires inspection and approval again. Plans contain no token, continuation,
command arguments or environment. Formatting whitespace is allowed; canonical
fields, ordering and values must remain intact.

An interrupted once hook is not proof of success. If no genuine success receipt
or proof establishes its outcome, inspection requires an explicit
`--rerun-once member` choice (comma-separated for several members). Review the
hook's external effects before approving that choice. This uncertainty survives
another stop even when the replacement epoch has not started any new command.
Known successful stages can supply genuine preparation proof without repeating
their hooks. Valid once receipts retain their exact original tuples. Changed
inputs require current preparation; unknown history never becomes success.

Claim repeats the observations under the launch/stop gate and SQL transaction,
checks growth admission, journals approval and creates a fresh lease capability
and continuation. Proof dependencies stay held by the new lease; other retained
history stays protected in the repair marker until final readiness. No history
or capture is deleted or released by takeover. This partition permits repair
after the ordinary held-stage budget is full.

Continue prepares every required member, confirms the genesis generation
remotely and atomically completes the journal, releases all retained history and
publishes ready without a lease. If a checkpoint generation was already pending,
it retries that exact vector without setup hooks or a new generation. Failures
preserve files, evidence and ownership. Cancellation after authorization requests
a synchronous stop and returns no fresh grant. A stop/fencing failure remains an
error requiring investigation; cancellation itself is not evidence of an empty scope.

Exact claim replay can recover the same still-active grant after a lost reply.
Once stopped or superseded it cannot revive that owner: inspect and approve a
fresh repair ID. Completed continuation replay authenticates the original
capability digest and returns a capability-free result only while the completed
workspace remains ready and unleased. A subsequent ordinary owner blocks old
repair result disclosure while its lease is held. Original create/recover receipts retain their original
response bytes and never rebind to the repair capability. Raw staging, warming,
preparation and lifecycle entry points cannot bypass the repair epoch.

Schema 20 retains schema-18/19 history and service receipts. Old binaries refuse
the upgraded state; migration refuses an active older supervisor. Stop with its
installed binary before upgrading. This local mechanism does not add a public
treaty operation or establish consumer acceptance of grouped v2.

Native/Linux checks and disposable XFS/cgroup crash-repair drills passed;
see [verification evidence](initialization-repair-output.txt). Phase 1
also requires laptop preparation, original task-ref transfer, broader collection
and an opt-in suite pilot.
