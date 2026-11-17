# Fresh allocation recovery: design

Status: proposed, revised after an independent review (rev 2). Phase 1, step A.
SuperBench 0.0.1's pilot runs on the Fresh (isolated-payload) path, and the operator
ruled that recovery lands before Fresh mode is switched on.

## Why

A one-shot Fresh bootstrap can fail or be cancelled after it has reserved quota and
created files. Today only an *absent* leaf can be retired
([fresh-absent-retirement](../../fresh-absent-retirement.md),
[fresh-bootstrap-recovery](../../fresh-bootstrap-recovery.md)). A failure that leaves
files, an empty leaf, or a project whose quota limits were never set stays parked
forever, and nothing can release its quota. Fresh mode must not be switched on while a
crash can wedge storage that way.

## What a failure leaves behind

Each phase is written before its effects, except `begun`, which is written after them.
The leaf is `DataRoot/benches/KEY`, and `benches` is root-owned and not group- or
other-writable.

| Intent phase | What can be on disk | Recovery path |
|---|---|---|
| `reserved`, no ledger row (crash between `reserve()` and the save) | nothing | Terminal: no quota is held. Documented, no action. |
| `reserved`, row saved | leaf absent; **or** an empty root-owned `0700` leaf with project ID 0 (crash between `mkdir` and `project -s`) or the assigned ID | leaf removal (§4), then quota repair (§5) when no report row exists, then absent retirement |
| `allocating` (a) | empty root-owned `0700` leaf | leaf removal, then absent retirement |
| `allocating` (b) | root-owned `0700` leaf holding a prefix of the fixed directories, daemon-owned except possibly the last, which is still root-owned | **fixed-name removal** (§4b), then leaf removal, then absent retirement |
| `allocating` (c), and `begun`, `staging`, `materialized_unfenced`, `staged` | daemon-owned leaf at the recorded inode, plus the fixed directories and Git trees, all daemon-owned | **daemon-owned cleanup** (§2–3), then leaf removal, then absent retirement |
| `transferring` | mixed root, daemon and payload | **out of scope**: stays parked |
| `completed` | registered layout | not a leak; out of scope |

`KEY.controls` and `git-controls` live on the ledger filesystem and carry no quota. They
stay as evidence. Planning requires `git-controls` to be empty or fenced
(`gitcontrol.Recover`). The live-process scan in the maintenance gate already refuses a
live daemon-uid Git.

## Goals

- Recover every phase from `reserved` through `staged` to a retired tombstone, using only
  operator commands run as direct root. There is no service route and no sudo route.
- Root never deletes recursively and never deletes a file. Root removes only empty
  directories with known fixed names, relative to held fds. Contents are deleted by a
  worker that has dropped to the uid that already owns them.
- Every effect is bound to the original operation, its permanent denial, the installed
  policy, the same helper binary, the stopped and masked service gate, and a durable
  retained approval written before the first effect.
- Deletions are durable on the data filesystem before quota is retired.
- A retry is safe after any crash and after a lost reply. A completed step that is
  retried with its matching retained approval reports success.
- Each new refusal has an ordinary-CI test where one is possible, and a privileged
  fixture assertion where root is required. Each must fail when its guard is removed.

## Non-goals

- The `transferring` mixed-ownership tree, and payload-owned cleanup.
- Turning `proposedFreshPayload` on. That is step D.
- Repairing operations made by a different helper binary. The same-binary rule stays.

## Design

### 0. Shared rules

- **Selection.** Every command selects the allocation through
  `freshretirement.selected`, never from `Assignments` alone. That requires: format 3,
  a canonical ledger, the exact Fresh marker, ID 10000 or above, and not retired.
- **Retained files.** Every approval is published atomically and exclusively:
  1. write a temp file in the target directory, fsync it;
  2. `link()` it to the final name (EEXIST means compare the bytes for an exact retry),
     unlink the temp file, fsync the directory;
  3. verify a root-owned `0600` regular file with `nlink == 1`, same file by name and fd,
     and the expected size.
  This replaces `retainRetirementFile`'s in-place `O_EXCL` write for the new, larger
  files. The size limit is per file type: the cleanup approval up to 16 MiB, the others
  64 KiB.
- **Plans are not authority.** `KEY.plan.json` may be replaced freely while no retained
  `KEY.json` exists. A plan is accepted at effect time only by its exact SHA-256.
- **Errors after retention** are wrapped: "effects may have occurred; retry the same
  plan".

### 1. Owner-parameterised cleanup approval (extends #85's `freshcleanup`)

- `Approval` gains `Owner {UID, GID uint32}` and the quota `Assignment {ProjectID, Bytes,
  Inodes}`.
- `validate` checks every entry and the root against `Owner`. `New` takes the owner, and
  the caller derives it from the phase: the daemon for `begun` through `staged`, and for
  `allocating` (c) once the leaf is observed daemon-owned at the recorded inode. Any
  other owner is refused.
- New per-entry refusal: directories must have owner `rwx`, and regular files owner `r`.
  Otherwise the daemon worker could not traverse or re-hash the entry, and a retained
  plan would wedge on `EACCES`. A plan with such an entry is refused **before**
  anything is retained.
- The existing rules all stay: the binding equals the retained inspection, the root is
  `0700` at the recorded device and inode, the parent is root-owned and not writable,
  each entry has a unique inode, only directories, regular files and symlinks are
  allowed, regular files and symlinks must have `nlink == 1`, and the order is canonical.

### 2. Plan: `plan-clean-bootstrap`

`bench-quota plan-clean-bootstrap KEY OPERATION_ID EPOCH STATE_IDENTITY AUTHORITY BINARY_SHA256`,
with the original Spec on stdin. Inside `MaintainDenied` and the gate:
1. Read the retained inspection, and require an allowed phase and owner (§1).
2. Require `git-controls` to be empty or fenced.
3. **The walk runs in the daemon-uid worker** (§3) in read-only `observe` mode. Root
   never opens the tree's files, so a FIFO swapped in can't block root while it holds
   `operations.lock`. The worker returns the `reap.Tree` plus uid, gid, nlink and mode
   per entry, using `O_NONBLOCK`, `O_NOFOLLOW` and fd-relative `Lstat` beneath an
   `os.Root`.
4. Root validates the result as data: size-bounded, through `freshcleanup.New`.
5. Root publishes `KEY.plan.json` and prints only the path and the SHA-256.

### 3. Effect: `clean-bootstrap`, and the daemon-uid worker

`bench-quota clean-bootstrap …`, with `{Spec, PlanSHA256}` on stdin. Inside
`MaintainDenied`, it runs:
1. gate → inspection equals the plan's binding → **retain** `KEY.plan.json`'s bytes,
   matched by SHA, as `LEDGER/payload-cleanups/KEY.json` (§0);
2. gate → worker in `delete` mode → gate;
3. re-observe through the worker: the root holds no entries, and its device, inode and
   mode are unchanged;
4. `fsync` the leaf directory fd, so the unlinks are committed through the ordered XFS
   log.

A retry after the leaf is already gone, with a matching retained approval, reports
success. Leaf removal re-checks it.

**Worker.** `bench-quota` re-executes **`/proc/self/exe`** (its verified running image)
as `clean-worker`, using `exec.Cmd` with:
- `SysProcAttr.Credential{Uid, Gid: daemon, Groups: []}`;
- `Pdeathsig: SIGKILL`;
- its own transient cgroup (`UseCgroupFD`), so a crashed parent can't leave an orphan
  that wedges `noControllers`;
- a clean environment, `Dir=/`, and only stdin and stdout and stderr.

Root sends `{mode, bench path, approved tree}` as a size-bounded frame and reads a
size-bounded reply, which it treats as data. The child:
- dispatches `clean-worker` before any flag or policy parsing;
- sets `PR_SET_NO_NEW_PRIVS` (Go's `SysProcAttr` has no field for it);
- verifies it is the daemon uid and gid, with no supplementary groups and all capability
  sets 0, through `/proc/self/status` as `payloadexec.verifyDropped` does;
- verifies `/proc/self/fd` holds only 0–2.

In `delete` mode it runs `reap.DeleteContents(root, approved)`, which deletes in reverse
order beneath an `os.Root`, re-inspecting each entry, and leaves the root itself.
`reap.Remaining` accepts approved entries that have disappeared and refuses anything
changed or new.

### 4. Leaf removal: `remove-empty-bootstrap-leaf` (and 4b, fixed names)

Root opens `benches` `O_DIRECTORY|O_NOFOLLOW` and checks it against the pinned parent
identity. It then `openat`s the key with `O_DIRECTORY|O_NOFOLLOW` and `fstat`s it. The
leaf must be:
- empty, by reading the directory;
- owned by root or the daemon as the phase allows (§table);
- on the recorded inode; for `reserved` only, a root-owned `0700` directory whose project
  ID is 0 or the assigned ID.

Then `unlinkat(parentfd, key, AT_REMOVEDIR)`, then `fstat` the held leaf fd: `nlink == 0`.
Then `fsync(parentfd)`. A non-empty directory makes `rmdir` fail on its own, which is the
backstop.

**4b. `allocating` (b).** Root removes **only the fixed directory names**, in reverse
creation order, each empty, relative to the held leaf fd, with `unlinkat(AT_REMOVEDIR)`.
The daemon can't enter the root-owned `0700` leaf, so these directories were never
exposed to it.

Leaf removal retains `KEY.leaf.json` before its effect. Retrying after a successful
removal, with ENOENT and a matching retained approval, reports success.

### 5. Quota repair: `repair-bootstrap-quota` (`reserved` only)

This covers a project whose limits were never set, so the kernel has no report row.
It runs only for phase `reserved` with the leaf already **absent** (after §4). For any
later phase a missing row means the kernel or mount state changed, so it refuses.

Under the allocation lock, permanent denial and the gate, it first proves:
- statfs reports XFS;
- the root is a dedicated mount: findmnt `FSROOT=/`, and the target equals `p.Root`;
- the mount UUID equals the ledger's `FilesystemUUID`;
- `state -p` shows accounting and enforcement on (as `apply_linux.go` does);
- the storage authority is unchanged;
- the selected Fresh assignment is not retired;
- the leaf is ENOENT below the pinned parent, with no child mount.

Then it retains its approval and runs `xfs_quota limit -p bhard=<Bytes/1024>k
ihard=<Inodes> <project>` with exactly the original caps. Read-back requires exactly one
row per unit with zero usage, those hard caps, and **soft limits of 0**. A retry that
finds rows with the exact caps reports success without doing anything; any other
existing row refuses. It never runs `Apply`, `mkdir`, `project -s`, a chown or an
adoption.

### 6. Retirement durability

Before it trusts ENOENT, `freshretirement.RetireAbsent` runs `syncfs` on the data root,
so leaf and content removal are durable before the tombstone is saved on the ledger
filesystem.

### 7. Code layout

- `payloadbootstrap` is at 3994 of 4000 lines, tests included. **Before** adding
  anything, move the retirement and inspection command glue out into a new package, so
  the narrow accessors fit.
- New code lives in:
  - `internal/freshrecovery`: plan and effect, leaf, fixed-name removal and repair, all
    root-side;
  - `internal/freshcleanup`: the extended model;
  - `internal/cleanworker`: the child's logic, used by `cmd/bench-quota`.
- The architecture allow-list gains these edges explicitly. Nothing reaches them from
  `benchd` or the payload helper.

## Testing

**Ordinary CI, unprivileged:**
- model validation for every refusal, including owner, phase, permission bits and the
  assignment binding;
- the worker's frame and self-check refusals;
- repair precondition parsing;
- atomic retained-file publication: link, EEXIST and compare, partial temp file;
- `reap` deletion and observation of a temporary tree, which already runs as non-root.

**Privileged XFS fixture (`freshstoragefixture`)** runs the real commands against a real
leak in each phase, recovering each to a retired tombstone:
- `reserved` (absent; empty with project ID 0; empty with the assigned ID);
- `allocating` (a), (b) and (c);
- `staged` with Git trees.

It also covers:
- a crash after each retained approval, and a partial temp file;
- a lost reply, and a retry after each step completes;
- an entry changed or added between plan and effect, a symlink swap, an unreadable file,
  and a directory without search permission (refused at plan time);
- a non-empty leaf given to leaf removal;
- repair refusals: accounting off, wrong UUID, a row present, cap mismatch;
- a `transferring` tree, which must stay parked;
- a worker killed mid-delete, followed by a retry.

Every guard gets a single-change mutation that must make the fixture fail at its own
assertion. Merge only after the fixture passes on the exact head.

## Delivery

Three PRs, each reviewed and fixture-proven on its exact head:
1. Retained-file publication, plus the `payloadbootstrap` split.
2. Leaf removal and fixed-name removal, `syncfs` in retirement, and quota repair.
3. The cleanup model extension, the worker, plan and clean.
