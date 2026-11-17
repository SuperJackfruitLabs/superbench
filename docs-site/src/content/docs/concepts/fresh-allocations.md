---
title: Fresh allocations and recovery
description: Fresh payload materialization is disabled. What exists today is direct-root inspection of retained bootstrap evidence and retirement of an absent Fresh allocation.
status: wet paint
---

A **Fresh allocation** is the storage a future payload-user bench would be bootstrapped into, using
a fresh registered layout rather than the shared service-account one. **Fresh payload
materialization remains disabled.** It stays disabled until interrupted or cancelled Fresh
allocations have an operator release or reconciliation path, because a failed one-shot bootstrap
can leave an allocation behind without a committed project ID.

What exists today is maintenance for that case, run by a **direct root operator** on Linux, through
the quota helper. None of it is reachable through sudo, the daemon or the API.

## Inspecting retained bootstrap evidence

```sh
bench-quota inspect-bootstrap-payload \
  KEY OPERATION_ID EPOCH STATE_IDENTITY AUTHORITY BINARY_SHA256 < original-spec.json
```

It takes the exact original helper identity and the original canonical bootstrap specification
on standard input, and reports the retained intent: its phase, recorded allocation, node identities
and command metadata, bound to the specification by SHA-256 digests. It refuses sudo callers,
policy overrides, unknown identities and missing, partial or foreign intent records. The original
installed policy, registry, allocation authority and helper binary must still match; do not
reinstall an old binary to get past a refusal.

Inspection releases nothing, repairs nothing and authorizes no retry. Its output is recorded
metadata, not a current filesystem inventory or a quota check: a `completed` phase reports retained
intent, not a revalidated tree.

## Retiring an absent allocation

`bench-quota plan-retire-bootstrap` (same identity arguments, original specification on standard
input) returns a `RetirementPlan`. It needs the original operation to be permanently denied, and
the installed service to be stopped and persistently masked with no daemon, payload or helper
processes left. The commands check those conditions; they do not stop, mask or configure anything.

After reviewing the whole plan, submit `Spec` and `Plan` as JSON to `bench-quota retire-bootstrap`.
Retirement holds the operation lock, rechecks the denial, the retained provenance and the
controller authority, and saves the exact reviewed plan as a root-owned 0600 file at
`LEDGER/payload-retirements/KEY.json` before changing any reservation. A retry must match it
exactly.

Only an **absent** fixed leaf, with XFS independently reporting zero blocks and zero inodes, can be
retired. The key, project ID and Fresh marker stay retired forever; only the reservation drops to
zero. No pathname is deleted and no kernel quota is changed. A refusal can happen after the plan
is saved, so an error does not mean no effects occurred: keep the plan and retry it.

## Not built

- Cleanup of nonempty Fresh allocations.
- Recovery when the original evidence is missing, and never-initialized projects with no quota
  report.
- The installed maintenance command lifecycle on a booted host. The privileged fixture tests cover
  the storage layer on disposable XFS; they do not prove that lifecycle.
