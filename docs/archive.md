# Archive and reviewed reap

Archive records a released, clean, remote-confirmed task checkout as unavailable
for new leases/runs. It preserves all files, including ignored build artifacts.
A persisted grace deadline blocks new deletion intents for seven days by default.
After that deadline, reap removes only the files in an explicitly reviewed plan. Neither operation is
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

## Grace period

The first archive records `ArchivedAt` and `ReapAfter` in authoritative state.
The default grace is seven days. A trusted local operator can choose a duration
from zero to 365 days on that first archive, for example:

```sh
bench archive --archive-grace 336h --state /absolute/state --data /absolute/data task
```

Explicit `--archive-grace 0s` waives the time delay for that new archive, but still
requires the reviewed file manifest and exact digest before deletion. Repeating
archive never changes an existing deadline, even if a different duration is
supplied. There is no reap flag to override it. You can inspect a plan during the
grace period; an early reap is refused before publishing an approval journal or
removing files. A new plan binds the persisted deadline into its `Digest`.

The clock boundary uses the host's UTC clock. This is not protection against an
operator changing that clock or private state, nor is it permission to expire a
lease. Deadline passage alone does not schedule deletion. Automatic completion/
merged-branch archive and read-only filesystem enforcement remain unfinished:
archive currently prevents SuperBench acquisition but does not remove filesystem
write permission from the trusted owner or other existing processes.

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
schema 9 makes earlier binaries refuse this archive policy. New plans use
`superbench.reap.v2` and bind `ReapAfter`; a changed deadline invalidates approval. Reap never resets a dirty
checkout to make its checks succeed. The current stopped/released-writer boundary
remains cooperative for processes launched outside SuperBench; stop those writers
before approving a plan. This is not uid/mount isolation against a hostile writer.

## Upgrade from earlier state

Stop active supervised executions with the installed binary before upgrading;
the existing migration guard refuses an active old supervisor. Read-only status
does not migrate. Use the new CLI's `migrate` command or a mutating operation to
upgrade to schema 9. Existing archives without deadlines receive seven days from
their original archive timestamp, so an already-old archive is not reset to a
new seven-day clock. Invalid timestamps refuse migration.

An unstarted v1 plan needs a fresh v2 review after upgrade. A v1 deletion intent
already committed in the old state may resume only with its exact saved journal
and digest. That exception preserves recovery of already-approved partial deletion;
it does not admit a fresh v1 approval. New or changed files still stop that retry,
and a completed retained intent remains idempotent. Earlier binaries refuse the
new state rather than ignoring its deadline.

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

Archive-grace verification: [archive-grace-output.txt](archive-grace-output.txt).
`test-archive-grace.py` uses the published schema-8 CLI to begin a genuine v1
reap, kills it after approved data removal but before finalization, then migrates
and resumes the original journal. It also checks default timing, immutable archive
retry, forged deadlines, explicit zero grace and downgrade refusal. Race tests
cover the exact expiry boundary, malformed/missing timestamps, migration from the
original time, rejection of fresh v1 approvals and preservation of new files on
an old-intent retry.
