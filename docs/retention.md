# Run history and reviewed log collection

New supervised runs record their execution ID, bench, holder and start time in
the same SQLite transaction that acquires the lease. No holder token or command
arguments are copied into history. A finished log is sealed with its file
identity, content hash, size and observed exit/truncation outcome. Successful
checkpoint/release records the confirmed checkpoint and release time atomically
with clearing the lease. Failed commands retain their lease and protected output.

State schema 6 protects this history/collection state from older binaries. Stop
old supervisors with the installed binary before upgrading. Existing history is
not invented for old logs; unregistered files remain protected until separate
operator inspection and approval. History and collection commands currently use
the local administrative CLI, not a new socket API or suite contract.

## Inspect history

```sh
bench history --state /absolute/state --data /absolute/data --limit 100 task
bench history --state /absolute/state --data /absolute/data
```

`ReleasedAt` is absent/zero until explicit release. `File` stays null until log
contents are sealed. A supervisor crash can therefore leave a held, unsealed
record even when its output file exists. Age never releases a lease or proves
that a writer stopped.

After stopping the recorded scope and explicitly checkpointing/releasing, a log
whose parent died before sealing can be reconciled:

```sh
bench reconcile-log --state /absolute/state --data /absolute/data EXECUTION_ID
```

Reconciliation requires a released record, no held execution referencing that
ID, and exclusive access to the log's writer lock. It hashes the remaining bytes
without inventing an exit status or claiming complete output. An already sealed
log cannot silently be resealed to different contents. Active/held logs remain
protected, including failed runs awaiting reconciliation.

## Review a collection plan

```sh
umask 077
bench plan-collect --state /absolute/state --data /absolute/data \
  --older-than 168h --metadata-older-than 720h --limit 100 > /private/log-plan.json
```

The plan lists exact log records/files and any previously collected history or
completed collection summaries old enough to prune. Log age is measured from
both release and sealing, so newly reconciled output gets its own review window.
Metadata age is measured from collection. Directory/file ownership, private permissions,
regular-file type, mount identity, inode/device and content hashes are checked.
Open writer locks, changed logs and held execution IDs prevent collection.

Review the records, file hashes and metadata deletions, then pass the exact digest:

```sh
bench collect --state /absolute/state --data /absolute/data \
  --plan /private/log-plan.json --approve FULL_REVIEWED_DIGEST
```

There is no automatic deletion in this checkpoint. Choosing `--older-than 0s`
only makes already released/sealed output eligible for review; it does not bypass
writer or lease checks. A wrong digest, changed file or stale history record
refuses before deletion. Unknown files, SQLite databases, lease records, creation
intents, reap journals, shared caches and mirrors are not collection targets.
No privileged quota helper recursively removes output files.

The collector first commits its complete approval plan and marks selected log
records as collecting. A second transaction rechecks all remaining files while
holding exclusive writer locks, removes only those exact files, fsyncs the state
directory and marks the collection complete. Missing files are accepted only
when resuming an already durable approval. A crash between unlink and metadata
commit retains a prepared plan; SQLite rollback does not restore a deleted file,
so the saved plan is the recovery authority.

```sh
bench collections --state /absolute/state --data /absolute/data
bench resume-collect --state /absolute/state --data /absolute/data \
  --approve COLLECTION_DIGEST COLLECTION_DIGEST
```

A completed retained summary makes retries idempotent. After approved summary
pruning, an old plan cannot become a fresh deletion command: its original record
must still be eligible and match the approved revision/file identity.

To abandon preparation and preserve remaining files:

```sh
bench cancel-collect --state /absolute/state --data /absolute/data \
  --approve COLLECTION_DIGEST COLLECTION_DIGEST
```

Cancellation never deletes a remaining file. It acknowledges files already absent
after an interrupted attempt, clears preparation for remaining records and
increments their approval revision. Old approvals stay revoked even after the
canceled summary is later pruned. Changed retained contents remain protected for
inspection; cancellation does not reset or overwrite them.

## Unregistered legacy logs

Old logs do not acquire a fabricated bench, command outcome or completion time.
First stop their old writers as part of upgrade/reconciliation, then inspect an
exact file in the fixed `run-<execution-id>.log` namespace:

```sh
bench inspect-log --state /absolute/state --data /absolute/data \
  EXECUTION_ID > /private/legacy-log.json
bench adopt-log --state /absolute/state --data /absolute/data \
  --plan /private/legacy-log.json --approve FULL_REVIEWED_DIGEST \
  --acknowledge-stopped-writers
```

Adoption requires separate content/ownership approval and cannot target a held
execution or replace an existing history record. It registers provenance as
`operator-adopted`, under the `unregistered` history key. Start time and exit code
remain unknown. It does not checkpoint, release or alter a bench lease. A fresh
collection plan still needs its own approval before deleting the adopted file.
The acknowledgment records operator responsibility for legacy writers that did
not participate in the new lock protocol; it is not an API privilege flag.

## Bounds and remaining retention work

History is capped at 10,000 records. Reviewed pruning of previously collected
records permits continued operation; those IDs do not authorize command replay.
Plans contain at most 1,000 total items and are bounded to 16 MiB. Pending plan
payloads share a 16 MiB budget, and retained collection summaries are capped at
10,000. Complete/canceled plans retire their large payloads while retaining the
summary. Use smaller plans if state write headroom or file descriptors are tight.
SQLite freed pages can be reused; collection does not promise database truncation.
Missing/corrupt evidence is preserved for inspection rather than guessed away.

These operations never delete service request receipts. Old opaque request IDs
still prevent redispatch after log/history collection. Sustainable receipt
collection needs the separate producer-first retry-window contract described in
[the implementation plan](implementation.md). Automatic retention policies,
shared-cache/abandoned-clone collection and creation/reap journal collection remain
subsequent work. This slice does not claim Phase 1 complete.

## Verification

The regular race suite checks history ownership/outcome, held failure protection,
writer locks, stale/changed approvals, cancellation revocation after summary
pruning, metadata pruning and real subprocess SIGKILL after unlink. Reopening the
SQLite database recovers the prepared approval and finishes without recreating a
log. Separate legacy adoption tests verify that no outcome is invented.

`scripts/test-retention.py` additionally exercises actual Linux cgroup execution
and daemon SIGKILL: stop/checkpoint/release/reconcile permit collection, and
pruning all history does not make the interrupted socket request executable
again. The test uses only private temporary data and a unique cgroup subtree;
no installed service or production data is changed.

Recorded results: [daemon/log collection](retention-linux-output.txt),
[storage/archive regressions](retention-storage-output.txt) and
[execution/full-state regressions](retention-execution-output.txt).
