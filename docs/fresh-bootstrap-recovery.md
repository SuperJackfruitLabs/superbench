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
