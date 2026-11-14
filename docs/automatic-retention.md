# Approved automatic log retention

`benchd` can periodically collect registered, released and sealed run output
under an explicitly approved policy. It starts disabled. Installing or starting
the service does not authorize deletion. These commands use the local
administrative CLI as the service account, with its exact state/data paths;
they do not add socket operations or suite contracts.

## Review and enable

Create a private preview with the first concrete batch and the rules for future
batches:

```sh
umask 077
bench retention-preview --state /absolute/state --data /absolute/data \
  --older-than 168h --metadata-older-than 720h --limit 100 \
  --max-collect-bytes 67108864 > /private/retention-policy.json
```

Review the entire `Policy`, `Initial` manifest and `Digest`. Approval authorizes
future eligible batches under those exact rules, not only the files in the
initial preview. Log age must be at least one hour since both release and seal;
metadata age must be at least 24 hours since collection. Each batch has at most
1,000 total log/history/summary items and at most the approved output byte count
(between 1 KiB and 1 TiB). The default is 100 items and 64 MiB. Metadata pruning
uses item/manifest bounds, not the output byte budget.

```sh
bench retention-enable --state /absolute/state --data /absolute/data \
  --plan /private/retention-policy.json --approve FULL_REVIEWED_DIGEST \
  --enable-automatic
```

Enabling requires the exact initial selection still to match a fresh preview.
Changed candidates require another review, except when reenabling the identical
approved policy to resume its existing exact pending batch. The policy is stored privately and
durably. No output is removed by activation itself. `benchd` checks on startup
and every minute by default; `--retention-interval` accepts one second through
24 hours. The Linux wrapper reads `BENCHD_RETENTION_INTERVAL` from its environment
file. Policy changes require another preview and approval.

The collector reuses [reviewed collection](retention.md): registered ownership,
released lease, sealed bytes, exclusive writer lock, unchanged file/hash and
record, private directory, mount checks, and durable approval before unlink.
Age never releases a lease. Failed runs remain protected while held. Unknown
logs are never automatically adopted. After separate legacy adoption, those
registered logs can qualify once their release/seal grace period passes.

## Observe, disable and recover

```sh
bench retention-status --state /absolute/state --data /absolute/data
bench retention-disable --state /absolute/state --data /absolute/data
bench retention-run --state /absolute/state --data /absolute/data
```

Status includes the approved configuration, pending exact batch and last attempt.
Disable waits for an active automatic batch to finish, then durably prevents new
automatic batches and retries, including after restart. It preserves pending
evidence. `retention-run` performs one cycle only if enabled. A concurrent cycle
returns `busy`; it does not launch another collector.

Before calling the core collector, maintenance saves its complete batch in
`retention-auto-pending.json`. Restart retries that exact plan, using the core's
prepared/completed collection evidence. It never silently substitutes a newly
discovered file list after an error. Failed checks leave the batch blocked and
report the error through status and daemon stderr. Oversize logs are deferred;
the last report lists deferred execution IDs. Candidate scanning is bounded by
the approved item limit, so increase the reviewed byte budget or use manual
collection if older oversized logs prevent useful batches.

For a blocked batch, disable the policy, inspect the exact pending manifest and
core `collections`, then explicitly resume or cancel that core digest using the
commands in retention.md. Cancellation preserves remaining output and revokes
the old approval. Once the core batch is completed/canceled, acknowledge the
maintenance pointer:

```sh
bench retention-ack-pending --state /absolute/state --data /absolute/data
```

Acknowledgment refuses while core evidence is still prepared. If no core entry
exists (for example a crash before core preparation), it can clear only the
maintenance pointer; it never deletes output or clears a core approval. A fresh
policy review can then proceed. Do not delete pending files or edit policy JSON
to bypass recovery. Ordinary shutdown joins the active maintenance cycle before
releasing the service's singleton lock; abrupt termination retains the two
durable recovery layers.

## Scope and evidence

This policy collects only registered output and already collected history or
completed/canceled collection summaries. It never collects service receipts,
lease records, creation/reap journals, caches, abandoned mirrors or worktrees.
Receipt replay protection remains unchanged. There is no production policy
enabled by this implementation checkpoint, and Phase 1 remains incomplete.

The race suite covers explicit enable, stale previews, fixed scope/age/byte
bounds, interrupted exact-plan retry, disabling during a batch, private policy
files, concurrency and canceled contexts. `scripts/test-automatic-retention.py` exercises actual Linux
cgroup runs and periodic daemon collection, including oversize output, held
failure evidence, unknown logs, explicit release and persisted disable. It
also verifies exact saved-batch replay after restart. It changes only a disposable
fixture; test-only SQLite timestamps simulate age
without waiting for the minimum retention period.

Recorded results: [automatic retention and disposable service installation](automatic-retention-linux-output.txt).
