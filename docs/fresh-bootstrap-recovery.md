# Retained Fresh bootstrap evidence

Fresh payload materialization is off unless `fresh_payload` is set in the root-owned
activation file (see [pilot installation](pilot-installation.md#activation)). Interrupted or cancelled Fresh
allocations are recovered by the direct-root operator steps below, from phase
`reserved` through `staged`; a `transferring` tree stays parked. Inspection
alone does not release quota, repair a workspace or authorize retry.

On Linux, a direct root operator can inspect the original bootstrap intent:

```sh
/usr/local/libexec/bench-quota inspect-bootstrap-payload \
  KEY OPERATION_ID EPOCH STATE_IDENTITY AUTHORITY BINARY_SHA256 < original-spec.json
```

Use the exact original helper identity and canonical bootstrap specification.
The installed binary path may differ with the installation prefix. This command
refuses sudo callers, policy overrides, unknown identities and missing, partial
or foreign intent records. Input is bounded and read before the operation lock.
The original installed policy, registry, allocation authority and helper binary
must still match. A changed installation or a historical allocation with missing
provenance requires separate reviewed recovery; do not invent an identity from
its key or project ID or reinstall an old binary to bypass a refusal.

The response binds the canonical specification and retained intent with SHA-256
digests, records the original operation's denial state, and reports the intent's
phase, recorded allocation, recorded node identities and command metadata. The
same operation lock covers the identity lookup, intent read and final checks.
Denied identities can be inspected but remain denied; an unknown identity is
never reserved to make inspection succeed. No response is emitted after a failed
read or final authority check.

Recorded metadata is not a current filesystem inventory, kernel quota check,
controller-fencing proof or completed-workspace receipt. In particular, a
`completed` phase reports retained intent rather than revalidating the tree.
A later maintenance action must independently establish stopped-controller and
storage custody, bind a reviewed cleanup plan, and recheck the evidence under
the same exclusion. Nonempty data and quota retirement are not handled here.

The privileged lower fixture selector is
`TestFreshIndexedMaterializerLowerFixture` with build tag `freshstoragefixture`.
Its inspection assertion is
`LOWER_RETAINED_BOOTSTRAP_INSPECTION_WITHOUT_REVIVAL_PASS`. It exercises the
Manager API against actual fixed policies, indexed history and an XFS allocation;
it does not prove the production command, booted systemd gate or pilot lifecycle.
Its `LOWER_READY_RECORD_VERIFIES_IN_PRIVATE_MOUNT_NAMESPACE_PASS` assertion opens
the indexed journal again from a new private mount namespace with its own `/tmp`,
where the data mount has another mount ID, as in a systemd unit with
`PrivateTmp` or `ProtectSystem`; the line logs both mount IDs.

## Denying the original operation

Every step below (plan-clean, clean, leaf removal, quota repair, retirement)
requires the create's original operation to be permanently denied first, so
the create can never resume. Deny it with the helper's own cancellation, as
direct root, with the original operation's five identity values:

```sh
/usr/local/libexec/bench-quota cancel-operation \
  OPERATION_ID EPOCH STATE_IDENTITY AUTHORITY BINARY_SHA256
```

Take the five values from the `Operation` field of the retained intent,
`LEDGER/payload-bootstrap/KEY.json` (`KEY@N.json` for generation N), and never
choose new ones. The reply is the operation's proof with `"Cancelled":true`;
`inspect-bootstrap-payload` then reports `OperationDenied: true`. Denial is
permanent: a cancelled operation is never revived. Like every other step, it refuses when the
installed helper binary or storage authority differs from the one the identity
names; do not reinstall an old binary to get past that.

## Absent-allocation maintenance

Direct-root `bench-quota plan-retire-bootstrap KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256`
reads the original canonical Spec on stdin and returns a `RetirementPlan`. The
original operation must already be permanently denied
([above](#denying-the-original-operation)). The fixed installed
service must actually be stopped and persistently masked, with no daemon,
payload or helper controllers in the original-host process inventory. These
commands check those conditions; they do not stop, mask or configure anything.
They accept neither sudo delegation nor a policy override.

Review the entire returned plan, retaining its original bootstrap/spec hashes,
operation identity and separate current allocation/parent evidence. Submit JSON
with the fields `Spec` and `Plan`, in that order, to
`bench-quota retire-bootstrap KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256`.
Both commands take the same original identity, not a newly chosen repair identity.
They require the same originally pinned helper binary and matching installed
policy. A changed binary or policy requires separate reviewed recovery; do not
reinstall an old binary to bypass a changed-policy refusal.
Input is bounded and read to EOF before acquiring the operation lock. The retire
request limit is 128 KiB; the plan command's Spec limit remains 64 KiB.

Retirement holds the existing operation lock, re-establishes the original denial's
durability, rechecks retained provenance and actual controller authority, and
persists the exact reviewed plan as a root-owned 0600 file at
`LEDGER/payload-retirements/KEY.json` before changing any reservation. An existing
approval must match exactly, including after a lost reply. The file is published
atomically: a complete, fsynced temp file in the same directory is linked to the
final name, so a crash leaves either no approval or a complete one, never a
partial one. Leftover `.KEY.json.tmp-*` files are harmless, and a retry removes
one that is still linked to an exact approval. Foreign or different approval
files refuse; retain them for explicit operator investigation. The commands never
automatically delete or replace `KEY.json`. If effects cannot be ruled out,
preserve the file and stop for separate recovery. The historical bootstrap intent is never
rewritten. A `reserved` intent continues to record zero allocation even when the
separate storage observation identifies a reservation. An unspecified requested byte quota must match the fixed policy's
original default.

Only an absent fixed leaf with independently proved zero blocks and inodes can
be retired. Before it trusts the leaf's absence, retirement runs `syncfs` on the
data root, so a removal is durable on the data filesystem before the tombstone
is saved on the ledger filesystem. The selected key, project ID and Fresh marker remain permanent; only
its reservation caps become zero. A refusal can occur after that save. Keep the
exact durable plan and retry it; an error does not mean no effects occurred.
Nonempty allocations and missing original evidence remain unfinished.
A never-initialized project with absent quota reports is repaired first (below).
These paths do not enable Fresh materialization. The lower fixture tests actual
approval-file custody and refusal without a booted maintenance gate; a successful
installed maintenance-command lifecycle still requires booted-host verification.

## Empty-leaf removal and quota repair

Two more direct-root steps bring an interrupted allocation to an absent leaf
with its original quota caps, so that absent retirement can finish it. Each is
plan, review, then effect, with the same six original identity arguments, the
same direct-root, no-sudo and no-`--policy` rules, the original Spec on stdin
for the plan, and `{"Spec":…,"Plan":…}` (canonical, at most 128 KiB) for the
effect. Input is read to EOF before any lock. Both run inside the original
operation's permanent denial, check the stopped and masked service before and
after every effect, select the allocation through the Fresh ledger rules (format
3, exact Fresh marker, project 10000 or above, not retired) under the allocation
lock, and require the retained inspection to be unchanged before and after.

```sh
bench-quota plan-remove-empty-bootstrap-leaf KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256 < original-spec.json
bench-quota remove-empty-bootstrap-leaf      KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256 < reviewed-request.json
bench-quota plan-repair-bootstrap-quota      KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256 < original-spec.json
bench-quota repair-bootstrap-quota           KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256 < reviewed-request.json
```

Before its first effect, an effect publishes the exact reviewed plan as a
root-owned `0600` file, `LEDGER/payload-cleanups/KEY.leaf.json` or
`KEY.repair.json`, with the same atomic link-and-fsync publication as retirement
approvals. An existing file must match exactly. After that point an error reads
"effects may have occurred; retry the same plan": keep the plan and retry it.
A retry after a lost reply returns the same result, which names the SHA-256 of
the retained approval.

**Leaf removal** opens `benches` with `O_DIRECTORY|O_NOFOLLOW`, requires it to
be the pinned parent, opens the leaf the same way relative to it and reads the
leaf's XFS project ID through the held descriptor. The phase decides what may be
removed:

| Phase | Leaf the step accepts |
|---|---|
| `reserved` | root-owned `0700`, project ID 0 or the assigned ID, empty |
| `allocating` (a) | root-owned `0700` at the recorded inode, assigned project, empty |
| `allocating` (b) | as (a), holding a prefix of the fixed directories Begin creates, each empty `0700`, daemon-owned except possibly the last, which may still be root-owned |
| `allocating` (c), `begun` to `staged` | daemon-owned `0700` at the recorded inode, assigned project, and only if already empty |
| `transferring`, `completed` | refused |

The fixed directories are removed in reverse creation order, each by
`unlinkat(AT_REMOVEDIR)` relative to its held parent, only while the name still
refers to the held, empty directory; then the leaf the same way. After each
removal the held descriptor must report the directory unlinked, and the parent
is fsynced. Root never removes a file, never removes a name outside Begin's
fixed list and never deletes recursively. Anything else in the leaf refuses
the plan, and the effect re-observes the leaf before it retains anything. A
retry that finds the leaf already absent succeeds only with the same retained
approval. A daemon-owned leaf that still holds Begin's directories or Git trees
is emptied first by the daemon-uid cleanup below.

**Quota repair** is only for phase `reserved` after the leaf is absent. It
proves the leaf is absent below the pinned parent with no child mount, the data
root is XFS and is the dedicated mount itself (findmnt `FSROOT` `/` and target
the data root) with the ledger's filesystem UUID, project accounting and
enforcement are on, and the authority is unchanged. With no report row for the
project it runs exactly `xfs_quota -x -c "limit -p bhard=<Bytes/1024>k
ihard=<Inodes> <project>"` with a fixed environment, then requires exactly one
row per unit with zero usage, those hard caps and zero soft caps. Rows that
already hold exactly those caps are an idempotent success. Any other row, a
nonzero usage, a soft cap or a foreign cap refuses. It never runs `Apply`,
`mkdir`, `project -s`, a chown or an adoption.

## Daemon-owned cleanup

A leaf the daemon already owns, holding Begin's fixed directories, Git trees or
anything else the daemon wrote, is emptied by a worker running as the daemon,
never by root. Root never opens the tree's files and never deletes a file.

```sh
bench-quota plan-clean-bootstrap KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256 < original-spec.json
bench-quota clean-bootstrap      KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256 < clean-request.json
```

Both follow the rules above: the original six identity arguments, direct root,
no sudo, no `--policy`, bounded input read to EOF before any lock, the original
operation's permanent denial, the stopped and masked service checked before and
after every effect, Fresh selection under the allocation lock, and an unchanged
retained inspection.

**Planning** requires a phase from `allocating` to `staged` and derives the
owner from it: the leaf must be the recorded inode, a daemon-owned `0700`
directory on the pinned device with the assigned project. For `allocating` this
is case (c), after Begin handed the leaf to the daemon; a root-owned leaf is
case (b) and uses leaf removal. `KEY.controls` must be the recorded root-owned
private directory, and its `git-controls` journal must be absent, empty, or hold
only unattributed controls whose cgroups are empty (the rules
`gitcontrol.Recover` uses, checked without changing anything).

A crashed Git step during materialization can leave an intent in
`git-controls` whose cgroup root no longer exists. The root is the service's
delegated cgroup, and systemd removes it when the stopped service's cgroup
empties. Planning refuses such an intent, by design, because it cannot inspect
a scope it cannot find. To proceed:

1. Read the `Root` field of each `LEDGER/payload-bootstrap/KEY.controls/git-controls/*.json`
   intent. Do not edit or remove the intent.
2. Confirm that the path is absent. Confirm also that the service is still
   stopped and masked: the gate checks this too.
3. As root, run `mkdir` on that exact path. On cgroup v2 this creates a new,
   empty cgroup. The intent's own scope, `Root/ID`, then reads as absent, which
   proves the old Git run ended.
4. Run `plan-clean-bootstrap` and `clean-bootstrap`.
5. Run `rmdir` on that exact path. Do this before the service is unmasked:
   systemd recreates it on start.

Never create the scope directory `Root/ID` itself. Never remove an intent to get
past this refusal. The worker then
observes the tree, read-only, and root validates its reply as a cleanup approval:
the binding equals the retained inspection, the owner is the daemon for the root
and every entry, the quota assignment is the selected one, the root is `0700` at
the recorded device and inode, the parent is the pinned root-owned `benches`,
entries are only directories, regular files and symlinks with unique inodes and
one link (files and symlinks), in canonical order, and every directory has owner
`rwx` and every regular file owner read, so the worker can always traverse,
re-hash and unlink what it approved. A file the daemon cannot read, a directory
it cannot search or write, a special file or a hard link refuses the plan before
anything is retained.

The plan is published as `LEDGER/payload-cleanups/KEY.plan.json` (root `0600`,
up to 16 MiB), atomically replacing an earlier plan; planning again is how a
plan is refreshed. The command prints `{"Path":…,"SHA256":…}`. A plan is not
authority: once `KEY.json` exists no plan can be published, and the effect
accepts a plan only by its exact digest.

**Cleaning** reads `{"Spec":…,"PlanSHA256":…}` (canonical, lowercase hex). It
finds the plan by that digest (the retained `KEY.json` when it exists), parses
it against the current inspection, owner and assignment, and has the worker
observe the tree again: a tree that changed in any way since review is refused
before anything is retained, so plan again. It then retains the plan's bytes as
`LEDGER/payload-cleanups/KEY.json` with the atomic link-and-fsync publication,
checks the gate, has the worker delete exactly the approved contents in reverse
order (re-inspecting each entry beneath an `os.Root`, never following a symlink
and never removing the leaf), checks the gate, has the worker observe the leaf
as empty with its device, inode and mode unchanged, and fsyncs the leaf. After
retention an error reads "effects may have occurred; retry the same plan". A
retry, after a lost reply, a crash or a killed worker, accepts approved entries
that are already gone and refuses anything changed or new. A retry after the
leaf itself has been removed succeeds only with the matching retained approval.

**The worker** is bench-quota's own running image, `/proc/self/exe`, started as
`bench-quota clean-worker UID GID` and dispatched before any flag or policy
parsing. It runs with the daemon's uid and gid and no supplementary groups,
its own session (no controlling tty, outside the operator's process group),
`SIGKILL` if the forking thread dies, an empty environment, `Dir=/`, only
descriptors 0-2, and a transient cgroup of its own that root kills, finds empty
and removes before it continues. Before creating one, root removes any empty
`superbench-clean-*` cgroup a crash left beneath the same parent; it never
touches a populated one. The cgroup goes beneath the payload policy's
cgroup root, the delegated root the bootstrap's Git runner uses; while the
service is stopped systemd may have removed that cgroup, and then it goes
beneath bench-quota's own cgroup. Root forks the worker from a locked thread
that first sets `no_new_privs`, empties its capability bounding set and joins a
new anonymous session keyring (`KEYCTL_JOIN_SESSION_KEYRING` with a NULL name).
Session keyrings belong to thread credentials, so only that thread and the
worker it forks leave root's keyring, and every worker thread shares the new one
for the worker's whole life. If root's own session keyring has already been
revoked or expired, for example by an ended login, it counts as none. Root then
passes the old keyring's serial, or 0, to the worker. Before
reading its request the worker makes itself non-dumpable (so no daemon-uid
process can ptrace it) and sets `no_new_privs`. It then lists `/proc/self/fd`
and requires only 0-2, and requires `/proc/self/stat` to show it leading its own
session with no controlling tty. It proves through the system calls and `/proc/self/status` that
it holds exactly the daemon's uid and gid in every slot, no groups and empty
capability sets, and refuses root. `/proc/self/status` must also show no
tracer. A process that attached between the exec and the worker becoming
non-dumpable stays attached, so this check closes that window; no attach can
succeed after it. Last, it requires its session keyring to be live and not the
serial root passed, so it never holds root's keyring. Only then does it write a
fixed ready line, and root sends the request only after reading that exact line.
Root removes the worker's cgroup on every return path. If bench-quota itself
dies, `Pdeathsig` kills the worker, and the next run sweeps the empty cgroup. It reads one bounded
request frame and writes one bounded reply, which root decodes as data and
quotes when it reports the worker's diagnostics.

## Recovery flow per phase

| Phase | What is left | Steps, in order |
|---|---|---|
| `reserved`, no ledger row | nothing | none: no quota is held |
| `reserved`, leaf absent | no files; the limits may never have been set | quota repair, absent retirement |
| `reserved`, empty leaf | root `0700` leaf, project 0 or assigned | leaf removal, quota repair (a no-op if the caps are set), absent retirement |
| `allocating` (a) | empty root `0700` leaf | leaf removal, absent retirement |
| `allocating` (b) | root leaf with a prefix of the fixed directories | leaf removal (fixed names), absent retirement |
| `allocating` (c) | daemon-owned leaf with the fixed directories | plan-clean, clean, leaf removal, absent retirement |
| `begun`, `staging`, `materialized_unfenced`, `staged` | daemon-owned leaf, fixed directories, Git trees | plan-clean, clean, leaf removal, absent retirement |
| `transferring` | mixed root, daemon and payload owners | parked: every step refuses |
| `completed` | a registered layout | not a leak; reclaim it once released |
| `reclaiming`, `reclaimed` | a reclaim's retained intent | reclaim only: rerun the same reclaim; after `reclaimed`, a create makes the [next generation](#generations-of-a-key) |

Every step in the table needs the original operation
[denied](#denying-the-original-operation) first. After absent retirement the key is free again: the next `create` on it makes
the key's [next generation](#after-a-failed-create-is-retired). `KEY.controls`,
its `git-controls` journal and every retained approval stay as evidence.

The privileged child selector `TestFreshRecoveryFixture` runs inside the lower
fixture (build tag `freshstoragefixture`) and recovers real leaks in each of
these shapes to a retired tombstone, including a `staged` leak with Git trees
made by the actual Begin and Materialize, with the real daemon-uid worker. Its
markers are listed in `internal/freshrecovery/fixture`. Like the other lower
fixtures it replaces only the booted maintenance gate.

## Reclaiming a released bench

Reclaim is not a recovery step: the daemon starts it, through the fixed
helper's sudo route (`bench-quota reclaim-payload`), after it has recorded the
bench `reclaiming`. Its operation identity derives from the workspace's key and
ID; root recomputes it and refuses any other. Its reservation holds
`operations.lock`, so no bootstrap, recovery or retirement step runs beside it.

What root trusts and what it checks:

- **Checkpointed: trusted from the daemon.** Whether the bench's work is
  checkpointed and durable lives in the daemon-owned mirror and the remote,
  which root cannot observe. A compromised daemon can have root delete work it
  never checkpointed.
- **Not in use: a point-in-time check by root.** Root refuses with "the bench is
  in use" while any cgroup beneath the policy's cgroup root, nested ones too, is
  populated with a process that has any task whose working directory, root,
  open file or file mapping is on the data volume in the bench's project, or
  while such a payload-account process runs anywhere. A malformed
  `cgroup.events`, an entry on the data volume root cannot attribute, and a
  process with live tasks root cannot read all count as use. Root checks before
  it retains `reclaiming` and again before the purge. This is a point-in-time
  check: a process can start between it and the purge. The project ID that
  attributes an open file to the bench is reliable because every payload
  process, the purge worker included, runs under the payload seccomp filter,
  which refuses project-ID and inherit-flag changes (see
  [Project quotas and the payload account](execution.md#project-quotas-and-the-payload-account)).
- **Contained: checked by root.** Before it retains `reclaiming`, again before
  the purge and again before it removes the emptied leaf, root walks the tree
  through descriptors only, never following a symlink or opening a FIFO, socket
  or device, bounded by the bench's inode cap. Every directory and regular file
  must carry the bench's project, and every directory the inherit flag.
  Otherwise the reclaim refuses with "the bench tree has entries outside its
  quota project" and changes nothing; restore the project as root
  (`xfs_io -x -c 'chproj -R PROJECT_ID' PATH`, and
  `xfs_io -x -c 'chattr +P' DIR` for a directory)
  and retry the same reclaim.
- **The guarantees are the backstops after the purge.** Root removes only an
  empty leaf, so anything written during the purge stops the reclaim, and it
  saves the tombstone only when the project's usage is zero, so a file still
  held open, or still charged to the project anywhere on the volume, stops it
  too. Either way the reclaim stays `reclaiming` and the same reclaim retries.
- **Provenance: checked by root.** The retained `completed` intent must match
  the request, and the leaf must be the bench's own (the recorded inode,
  payload-owned, in the assigned project) with fenced Git controls.

Then, in order, and each step safe to repeat:

1. the intent moves to `reclaiming`, before any effect;
2. the clean worker, through the same hardened spawn but as the payload account,
   deletes the tree: by name relative to held directories, never following a
   symlink or opening a FIFO, socket or device, never entering another device,
   mount, project or owner, at most 64 directories open, with a step bound;
3. root removes the empty leaf under the allocation lock;
4. root proves the leaf gone and usage zero, syncs the data filesystem and saves
   the tombstone;
5. the intent moves to `reclaimed`.

A completed bench whose leaf is already missing refuses; inspect it with the
steps above. Errors after step 1, and every error of a retry whose intent is
already `reclaiming`, say `effects may have occurred; retry the same reclaim`.

The purge worker runs as the payload account, so the payload can stop it. The
worker reports progress at least once a second; root kills one that reports
nothing for 30 seconds, instead of holding `operations.lock` until the 20-minute
worker timeout. The reclaim then fails with "the payload account is still active
in the bench", as it does when entries reappear after the purge or usage stays
above zero because a payload process still holds a file. Stop the bench's
processes and rerun the same reclaim.


## Generations of a key

A create on a key whose bench is `reclaimed` makes the key's next generation:
a new bench, never the old one again. Each generation has its own identity
and evidence, and the earlier generation's stays where it was.

| | First generation | Generation N (2, 3, ...) |
|---|---|---|
| Bootstrap spec | `superbench.payload-bootstrap.v1`, unchanged | `superbench.payload-bootstrap.v2`, naming `Generation` and `Previous`, the reclaimed `bench_id` it follows |
| `bench_id` | random | random, never the previous generation's |
| Intent and controls | `payload-bootstrap/KEY.json`, `KEY.controls` | `payload-bootstrap/KEY@N.json`, `KEY@N.controls` |
| Quota project | assigned once | a new ID from the ledger's counter |
| Layout registration | `payload-layouts/KEY.json` | the same name; the daemon first renames the previous one to `KEY.<previous bench_id>.reclaimed` |

The daemon records the new bench `materializing` in place of its `reclaimed`
record; that is the only change a `reclaimed` record allows. Every create and
reclaim step above then takes the generation's own spec, with the same six
identity arguments.

What root checks before a later generation's first effect, under the create
operation's lock and then the allocation lock:

- the previous generation's retained intent (`KEY.json`, or `KEY@N-1.json`) is
  canonical, names the same key, the `bench_id` the spec names as `Previous` and
  the generation before, is in phase `reclaimed`, was recorded under the same
  storage authority, and its leaf is this key's;
- no layout registration under the key is still published;
- the ledger row for the key is exactly that intent's project as a tombstone
  (zero caps) with its Fresh marker. In the same save that assigns the new
  project, the tombstone moves to the ledger's `Retired` list, where its ID is
  never assigned again; the ledger becomes format 4, which an older binary
  refuses to read;
- the leaf is absent, so the old tree is never adopted.

A second create of the same generation, a create of a generation that skips
one, and a repeat of the first generation's bootstrap all refuse with no
effect. A retry of the earlier generation's own reclaim after the next one
exists refuses too: its ledger row is the next generation's now. A later
generation that is interrupted is recovered with the steps above, using its
own spec. The cleanup and retirement approvals are named by the key alone
(`payload-cleanups/KEY.*`, `payload-retirements/KEY.json`), so a key's
direct-root recovery runs once: if a generation of a key whose earlier
generation was already recovered that way fails too, the steps refuse on the
existing approval. Keep everything as evidence and stop; that case needs
per-generation approval names, which do not exist yet.

The lower fixture proves this on real XFS after the reclaim cases. Its markers,
each logged alone on its line, are
`LOWER_RECREATE_REFUSED_WHILE_OLD_LAYOUT_PUBLISHED_PASS`,
`LOWER_RECREATE_FORGED_PREVIOUS_REFUSED_PASS`,
`LOWER_RECREATE_GENERATION_GAP_REFUSED_PASS`,
`LOWER_RECREATE_PREDECESSOR_BINDING_REFUSED_PASS`,
`LOWER_RECREATE_NEW_PROJECT_PASS`, `LOWER_RECREATE_FRESH_TREE_PASS`,
`LOWER_RECREATE_OLD_EVIDENCE_RETAINED_PASS`,
`LOWER_RECREATE_SECOND_SUCCESSOR_REFUSED_PASS`,
`LOWER_RECREATE_OLD_PROJECT_NOT_REVIVED_PASS` and
`LOWER_RECREATE_SECOND_GENERATION_RECLAIMED_PASS`. With the old layout already
moved aside, the forged, generation-gap, binding and second-successor cases are
refused by the deeper guards: the predecessor's intent, and the ledger.

### The source commit

A member's source is often not on any local branch of the daemon's mirror. The
mirror's refresh keeps the remote's branches under `refs/remotes/origin/*`, so
a card branch pushed past `main`, such as the branch an earlier generation
checkpointed, is there only as a remote-tracking ref. `git clone --no-local`
copies only the mirror's local branches. So after the clone, and before the
checkout, each member fetches exactly the source object ID from the same mirror
path, as the daemon account under the same fixed options and scope as every
other bootstrap command:

```
git -c protocol.version=2 fetch --no-tags --no-write-fetch-head --no-auto-maintenance --recurse-submodules=no --refmap= -- MIRROR SOURCE
```

`MIRROR` is the daemon's own path for the registered repository and `SOURCE`
the spec's validated object ID, which the mirror was already checked to hold as
a commit. The fetch creates no ref and no `FETCH_HEAD`, never contacts the
network and never writes the mirror; the bench gets only the branch the spec
names. Protocol v2 is set because it serves any commit the mirror holds, not
only the tips it advertises. The final check still requires `HEAD` to be the
branch at exactly `SOURCE`.

The lower fixture proves a Fresh create and a reclaimed key's next generation
from such a branch, with the markers `LOWER_FRESH_CREATE_FROM_MOVED_BRANCH_PASS`
and `LOWER_RECREATE_FROM_MOVED_BRANCH_PASS`.

### After a failed create is retired

A create that never completed (its intent stops before `completed`, and the
daemon's record stays `materializing` or `initializing`) also gives way to the
key's next generation, once root has retired its allocation with the steps
above: [deny](#denying-the-original-operation), clean, remove the empty leaf,
retire. The intent is never rewritten; it stays in the phase it stopped in.

The daemon never decides this from its own record. A `create` on the key with a
new request ID asks the fixed helper, before any plan or effect, with the
record's own canonical Spec on stdin:

```sh
/usr/local/libexec/bench-quota inspect-retired-payload KEY < failed-spec.json
```

The daemon runs it through its sudo route; direct root can run it the same way
to check a recovery. It is read-only and takes no identity, path or policy
override. Root answers only from its own retained files: the
intent of that Spec (`KEY.json` or `KEY@N.json`) is canonical, in a phase
before `completed`, under the current storage authority;
`payload-retirements/KEY.json` reviewed exactly that intent (its digest, spec,
phase, workspace, operation, policy and recorded allocation) under its denied
operation; and, under the allocation lock, the ledger row is that project's
tombstone with its Fresh marker and the leaf is absent. Its reply names the
key, `bench_id`, generation and the zero tombstone; the daemon checks all four.

On that reply, and only then, the create records the next generation in place
of the failed record (a new `bench_id`, `Generation` one more, `Previous` the
failed `bench_id`) and runs the bootstrap above. Root's checks before the next
generation's first effect accept the retired predecessor as they accept a
`reclaimed` one, with the same evidence again: no skipped generation, no
second successor, the tombstone moved to `Retired`, a new project, the leaf
absent. Until root's reply proves the retirement, the create is refused as a
conflict, as before, and the failed record is unchanged. No new phase appears:
a status read shows the failed record as it was until the next generation
replaces it.

The lower fixture proves this on real XFS: a create that fails while
materializing, root's recovery, root's inspection, the next generation and its
reclaim. Its markers, each logged alone on its line, are
`LOWER_FAILED_CREATE_REFUSED_BEFORE_RETIREMENT_PASS`,
`LOWER_FAILED_CREATE_ROOT_PROVES_RETIRED_PASS`,
`LOWER_FAILED_CREATE_NEXT_GENERATION_NEW_PROJECT_PASS`,
`LOWER_FAILED_CREATE_OLD_PROJECT_NOT_REVIVED_PASS` and
`LOWER_FAILED_CREATE_NEXT_GENERATION_RECLAIMED_PASS`.
