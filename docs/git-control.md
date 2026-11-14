# Git control operations and writer fencing

The schema-12 implementation could stop a supervised payload and then run Git
checkpoint commands outside its process scope. A pre-push or reference-update
hook could start a detached writer there. The command returned success and
released its lease while that writer remained alive and later changed tracked
files. Both paths were reproduced on a disposable Linux fixture.

## Managed control scopes

Linux supervised run/setup now carry a control runner through mirror validation,
configuration reads, checkpoint and release. Each Git control command gets its
own cgroup with host/boot identity. Before launch, a private record is fsynced
under `state/git-controls/<id>.json`, including the new directory's parent entry.
The child enters that scope atomically at launch. Completion fences every scope
descendant before returning, including detached filter/transport helpers.

Manual checkpoint/release of a supervised lease derive the control root from its
recorded execution. Creation, ordinary acquisition and archive also use control
scopes when their configured delegated root is available. The normal mirror
timeout applies (two minutes by default, configurable through one hour), and a
shorter caller deadline still wins. An error or deadline does not release the
lease or authorize another holder.

A controller crash leaves its record. Every mutating store transaction recovers
validated control scopes under the global state lock before it can grant,
checkpoint or release ownership. Recovery does not clear the bench lease. A
read-only status request does not kill a process. Records with foreign identity,
unsafe roots, invalid contents or failed fencing remain for reconciliation.
Recovery validates an actual cgroup filesystem before any fence operation; it
cannot turn a recorded path into an arbitrary filesystem write.

Even after reboot, a missing historical control root fails closed. Restore its
validated cgroup location before reconciliation; do not delete its record merely
because time passed. Process-scope fencing coordinates trusted commands and does
not claim isolation from the same uid or root.

## Command-local Git policy

SuperBench's own Git control commands disable client hooks, filesystem-monitor
helpers, replace-object interpretation and automatic following of unrelated
annotated tags. Ambient Git configuration/path overrides and lease-token
environment are removed. These options apply to that command only: repository
and host configuration are preserved. Server receive hooks remain active.

Developer hooks and intended checks can still run as explicit work inside
`bench run`, where their descendants belong to the payload scope. They must not
be silently launched after that payload's stopped-writer proof. Managed control
scopes additionally contain other Git helpers, such as configured content
filters; hook suppression alone would not provide that guarantee.

An unconfigured Linux host or native macOS caller retains cooperative control
behavior with the command-local policy, without claiming Linux helper fencing.
Supervised preparation still requires its supported execution backend. A
cooperative lease is not evidence that an externally launched writer stopped.

## Upgrade and verification

State schema 13 is committed before the first managed control child can start.
If its outer operation crashes or rolls back, this barrier remains, so a schema-12
binary cannot ignore a live control record and grant ownership. Stop active old
supervised commands with the installed binary before upgrade. Older binaries
did not record these Git workers: reconcile any old untracked helper writers
explicitly; migration cannot retrospectively prove they stopped.

The path validator and lease-record construction moved into small adapters to
retain the engine's existing size limit. Their behavior remains covered by the
existing tests and path/symlink regression cases. The v1 socket request shape
and its canonical fixture bytes are unchanged.

Required checks passed on macOS and Linux. Verification includes old-binary
reproductions, real managed Git/filter
descendants, user-hook execution inside a fenced payload, server-hook rejection,
controller-only SIGKILL, durable schema barrier, read-only status, recovery before
another mutation and retained ownership. All eight live Linux groups passed,
along with automatic archive and legacy grace/permission recovery regressions.
See [complete verification](git-control-output.txt) for commands, source/binary
provenance, full normalized output and cleanup. No installed service or
production data was changed.
