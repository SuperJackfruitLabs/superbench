---
title: Archive and reap
description: A released, clean, remote-confirmed bench can be frozen read-only, and deleted only after a grace period and a person's approval of the exact file manifest.
status: wet paint
---

Archive and reap are how a finished bench leaves the host. They are **local administrative
commands, reviewed by a person**; neither is a socket operation for single-repository benches, and
neither takes a lease over by time.

## Archive

Stop or reconcile any supervised execution, commit, checkpoint and release first. Then:

```sh
bench archive --state /absolute/state --data /absolute/data task
```

Archive checks the original mirror and branch, a clean tracked and untracked state, HEAD equal to
the saved checkpoint, and the same commit still on the remote task branch. It then makes the bench
unavailable to new leases and runs, keeps every file (ignored build output included), and removes
write permission from regular files and directories after saving a durable permission intent. An
archived key cannot be reused; recover the branch under a new key to resume work.

The read-only boundary is cooperative: ordinary writes are refused, but the trusted owner can
`chmod` the tree, root can bypass mode bits, and already-open writable handles are not revoked.

## The grace period

The first archive records `ArchivedAt` and `ReapAfter`. The default grace is seven days;
`--archive-grace` on that first archive can choose 0 to 365 days, and `0s` explicitly waives the
delay. Repeating archive never changes a recorded deadline, and no reap flag overrides it. An
early reap is refused before anything is written or removed.

## Review, then reap

Generate the plan without changing the checkout:

```sh
umask 077
bench plan-reap --state /absolute/state --data /absolute/data task > /private/plan.json
```

Read every `Tree.Entries` path, especially those marked `Recoverable: false`: ignored artifacts,
nested repository contents and anything else outside the commit are lost on approval. Then approve
the exact digest of that concrete plan:

```sh
bench reap --state /absolute/state --data /absolute/data \
  --plan /private/plan.json --approve FULL_REVIEWED_DIGEST task
```

The engine checks the whole current snapshot still matches the plan, syncs an approval journal
outside bench data, and commits its intent before deleting anything. New, replaced, edited or
special files stop deletion and stay for review. Reap refuses effective uid zero: run it as the
non-root service owner. It never pushes unknown files, auto-commits, or deletes the branch.

After an interruption, retry with the same plan and digest; only unchanged remaining approved
entries are removed. A completed reap keeps a tombstone, and repeating it is idempotent.

## Quota retirement

On managed XFS hosts, the worker removes the approved contents and the fixed root helper removes
only the empty bench directory, after checking its allocation ID and filesystem and confirming that
both blocks and inodes are zero. Open but unlinked files keep the reservation until they close.
Retired project IDs and keys stay retired. This applies only on managed Linux hosts.

## Automatic archive and retention

A repository can opt into automatic read-only archive after a verified unmerged-to-merged
checkpoint transition (`[archive]` in `.bench/config.toml`, see
[Repository settings](/reference/repository-settings/)). Deletion still waits for a reviewed reap
plan after the grace period.

Run logs, receipts and journals live outside the deleted checkout. Reviewed log collection exists
(`plan-collect`, `collect`), and the daemon can run an automatic log retention policy, which stays
**off until an operator reviews and enables it** (`retention-preview`, `retention-enable`).
Collection of shared caches, mirrors and request receipts is not built.
