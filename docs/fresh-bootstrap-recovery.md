# Retained Fresh bootstrap evidence

Fresh payload materialization remains disabled. Interrupted or cancelled Fresh
allocations are recovered by the direct-root operator steps below; a leaf that
still holds daemon-owned files or Git trees has no recovery command yet.
Inspection alone does not release quota, repair a workspace or authorize retry.

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

## Absent-allocation maintenance

Direct-root `bench-quota plan-retire-bootstrap KEY OPERATION_ID EPOCH STATE_ID AUTHORITY BINARY_SHA256`
reads the original canonical Spec on stdin and returns a `RetirementPlan`. The
original operation must already be permanently denied. The fixed installed
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
needs the daemon-uid cleanup worker, which is not implemented yet.

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

Typical orders: a `reserved` leak with the leaf absent and no rows: repair,
then retire. With an empty leaf: leaf removal, repair (a no-op if the caps were
already set), then retire. An `allocating` (a) or (b) leak: leaf removal, then
retire.

The privileged child selector `TestFreshRecoveryFixture` runs inside the lower
fixture (build tag `freshstoragefixture`) and recovers real leaks in each of
these shapes to a retired tombstone. Its markers are listed in
`internal/freshrecovery/fixture`. Like the other lower fixtures it replaces only
the booted maintenance gate.
