# Retained Fresh bootstrap evidence

Fresh payload materialization remains disabled. Interrupted or cancelled Fresh
allocations still require an operator release/reconcile path before enablement.
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
be retired. The selected key, project ID and Fresh marker remain permanent; only
its reservation caps become zero. A refusal can occur after that save. Keep the
exact durable plan and retry it; an error does not mean no effects occurred.
Nonempty allocations, missing original evidence
and never-initialized projects with absent quota reports remain unfinished.
These paths do not enable Fresh materialization. The lower fixture tests actual
approval-file custody and refusal without a booted maintenance gate; a successful
installed maintenance-command lifecycle still requires booted-host verification.
