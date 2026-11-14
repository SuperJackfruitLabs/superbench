# Archive and reviewed reap

Archive freezes a released, clean, remote-confirmed task checkout. It preserves
all files, including ignored build artifacts, and prevents new leases/runs. Reap
then removes only the files in an explicitly reviewed plan. Neither operation is
a time-based lease takeover or an automatic card-completion policy.

These commands are currently local administrative CLI operations. They are not
new socket RPC operations until a producer-first treaty extension is reviewed.
Do not bypass the installed daemon's uid policy by granting clients access to its
private state. Perform local administration as the trusted service owner/operator,
using exactly the state/data paths of that service.

## Archive

Stop/reconcile any supervised execution, commit intended source changes, checkpoint
and release the lease before archive:

```sh
bench archive --state /absolute/state --data /absolute/data task
```

Archive checks the original mirror/branch, clean tracked/untracked state, current
HEAD equal to the saved checkpoint and the matching branch commit still on the
original remote. It refuses a metadata-locked worktree. Ignored artifacts may
remain, but do not become recoverable through the checkpoint. Repeated archive
returns the retained record. An archived key cannot be reused; recover the branch
under a new key to resume work. Automatic merged-branch/card archive is later work.
A branch deleted after merge is not sufficient evidence here: the current
implementation requires the recorded remote task branch and exact checkpoint.

## Review and approve deletion

Generate the private plan without modifying the checkout:

```sh
umask 077
bench plan-reap --state /absolute/state --data /absolute/data task > /private/plan.json
```

Inspect every `Tree.Entries` path, particularly `Recoverable: false`. Ignored
artifacts, nested repository contents and other files outside the parent commit
can be permanently lost by approval. Copy anything that must be retained to an
approved durable destination before making a fresh plan. No archive/reap command
pushes unknown files, auto-commits or deletes the branch/ref.

The manifest records path/type/mode, device/inode identity and content hashes;
symlinks are hashed as links, never followed. `Recoverable` requires raw file/link
content matching a blob in HEAD, not just membership in the Git index; hidden
local edits and checkout-filter output are not incorrectly marked recoverable. It rejects nested filesystems and
special filesystem objects and paths that cannot be represented as UTF-8. Entries are bounded to 100,000 and encoded manifests
to 16 MiB; exceeding those limits refuses deletion. State space is checked before
saving the durable approval. These limits are not permission to omit any entries.

After reviewing the **concrete plan**, use its exact `Digest` as approval:

```sh
bench reap --state /absolute/state --data /absolute/data \
  --plan /private/plan.json --approve FULL_REVIEWED_DIGEST task
```

The CLI requires both the bounded JSON plan and matching digest. Duplicate JSON
fields, trailing documents, invalid UTF-8 and unknown plan fields are refused. The engine checks
that the entire current snapshot still matches, then fsyncs an approval journal
outside bench data and commits its reaping intent before deleting files. State
schema 4 makes earlier binaries refuse archived state. Reap never resets a dirty
checkout to make its checks succeed. The current stopped/released-writer boundary
remains cooperative for processes launched outside SuperBench; stop those writers
before approving a plan. This is not uid/mount isolation against a hostile writer.

## Retry, quota and evidence

After interruption, keep the same plan and digest. Only unchanged **remaining**
entries from that durable approval may be removed. New, replaced, edited or special
files stop deletion and remain for review. A partially deleted checkout cannot
silently acquire a fresh lease. The retained branch/checkpoint and approval record
remain visible. A successful reap retains a tombstone with archive/reap timestamps
and the digest; repeating that exact plan is idempotent.

On managed XFS, the worker removes approved contents and the fixed-policy root
helper removes only the empty logical bench directory. It checks its allocation
ID and the same filesystem UUID, then confirms both blocks and inodes are zero.
Open-but-unlinked files still retain their logical reservation. After they close,
retry the original reap. The helper never recursively deletes files or accepts an
arbitrary filesystem target. It cannot release shared cache/mirror reservations.

Released project IDs/keys stay permanently retired as zero-byte ledger entries;
new projects get higher IDs. These entries make older helpers fail their ledger
validation rather than resurrecting released allocations. The former hard limit
remains on the retired ID so interrupted retries can confirm the usage record.
Only its logical reservation is released. Keep the privileged ledger intact.
Lost replies after retirement can be retried before the core reap is finalized.

The core removes only this worktree's Git registration, preserving other benches,
local refs and remote branches. Run logs, receipts, creation/reap journals and
history remain outside the deleted checkout; their collection is a separate
reviewed retention operation. Reap is not cache or mirror garbage collection.

## Verification

The regular race suite covers clean/released/confirmed archive admission,
archived acquisition refusal, exact approval, changed/new ignored files,
symlink safety, interrupted deletion, old-approval replay and recovery of the
checkpoint under a new key. The privileged Linux drill is
`scripts/test-archive.sh` plus `scripts/test-archive.py`, inside a private mount
namespace with one disposable 512 MiB XFS image. It checks actual zero-usage quota
retirement, open-unlinked files (including zero-block open inodes), wrong IDs and
symlink refusal, lost reply/SIGKILL recovery, non-reused project
IDs and refusal by the previous CLI/helper. No existing bench or production
volume is a test target.

Recorded kernel results: [archive-linux-output.txt](archive-linux-output.txt).
CLI JSON admission is also exercised end-to-end by the regular suite, including
duplicate authority fields, missing/wrong approval and successful reviewed deletion.
