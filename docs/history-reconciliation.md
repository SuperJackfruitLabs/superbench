# Interrupted workspace history

After a daemon or local supervisor dies, an execution can remain owned with no
sealed outcome. Explicit administrative stop fences its recorded Linux scope and
revokes continuation. It does not establish an exit code or capture completeness.
Ordinary checkpoint/release must continue to refuse this missing evidence until
an operator reviews a concrete history observation.

Local administration provides:

```sh
bench workspace stop_admin --state /private/state --data /workspace/data card
bench workspace inspect-history --state /private/state --data /workspace/data card > history-plan.json
# Review the exact plan and copy its Digest.
bench workspace reconcile-history --state /private/state --data /workspace/data \
  --plan history-plan.json --approve DIGEST card
```

Use the actual configured state, data and cgroup-root settings. These history
commands require direct local state authority; `--socket` is refused. Inspection
requires an explicitly stopped owner and independently verifies its recorded
execution fence. It never implicitly resumes or retries the interrupted command.
The generated JSON plan may be formatted with whitespace, but its canonical
fields, values and order must remain intact. Approval is its exact `Digest`.

The plan binds the state root, immutable workspace ID, current private owner-row
digest, complete execution identity, original history digest, observation time
and exact log snapshot or absence. It contains no lease capability, continuation,
arguments or environment. A present log must be a private owned regular file
under its fixed run-ID name, within the recorded execution size limit. Hashing is
bounded and uses the locked descriptor. A live capture, changed file, symlink,
hard link, changed owner/scope/history or stale approval is refused. Absence is
recorded without creating an empty replacement.

Apply repeats ownership, fencing and snapshot checks under the launch/stop gate
and state transaction. The product writer lock remains held through commit.
Capture creation also uses the launch/stop gate, so a delayed current-version
runner cannot create a late capture after its authority was revoked.

Absence records what was observed at approval time. A delayed schema-18
controller can still create an empty file under its original fixed run ID before
its subsequent launch attempt refuses schema 19. That artifact is preserved;
the refusal prevents payload launch and history mutation, and absent-observation
metadata never authorizes log collection. Reconciliation does not claim that an
old controller was terminated or that no artifact can ever appear later.

The persisted `Interruption` is an explicit **unknown outcome** observation. It
preserves supervised origin, original start time, holder and run identity.
`ExitCode` remains null. `LogState` is `present` or `absent`; neither proves the
original capture's completeness or truncation status. It cannot become a setup
success proof, restore a continuation, change the lease, or clear a command's
replay-protection receipt. Exact approved retries return the recorded observation
without refencing a subsequent owner, including after checkpoint/release.

An ordinary ready workspace can then checkpoint and release under its existing
capability if all other lifecycle checks pass. Repairing stopped initialization
is separate: an unknown setup outcome cannot publish readiness. Present observed
logs become eligible for existing reviewed retention only after release and the
usual ownership/snapshot checks. Absent-capture metadata remains retained; no log
or metadata is silently deleted.

State schema 19 prevents older binaries from ignoring these observations. Stop
supervisors with their installed binary before upgrading; migration refuses an
execution still recorded as starting or running. Source-level tests do not
establish managed Linux behavior; verification evidence is recorded separately.
