# Creation preparation contract impact

Assessment for `feat/creation-warmup`, based on the Linux verification below. No consumer deployment or accepted shared-contract
claim is made by this document.

## Authority and inspected revisions

Charter accepted baseline `be8a10f01b08aee2455cff98aa33ad513a7e7570`
assigns Runtime/Environment to AgentPod. Its platform strategy distinguishes
Environment lifetime from an agent session. The proposed
`decisions/2026-11-14-superbench-owns-bench-lifecycle.md` records the operator's
standalone-product ruling, but labels the precise Environment ownership split
and integration boundaries as proposed for review. It does not replace the
accepted ownership row. Suite adapters must follow acceptance of those boundaries
and the wire proposal.

| Surface | Inspected revision | Role in this change |
| --- | --- | --- |
| SuperBench | `48ab88dcec9eabe39bcfc00954313b4aaa1ddafa` plus working changes | Producer, local CLI consumer, durable request executor |
| treaty | `e66ec6098980ca09472440bc6d5aebf241f43a35` (proposal) | Canonical schema and request/lifecycle vectors |
| AgentPod | `e9d6dddab4c6e90fcca5c98f97cb39b8da8bd7d0` | Future opt-in acquisition consumer; unchanged |
| SuperPipeline | `5501b2530824db8d668deafcad7977289b241008` | Future pilot orchestration consumer; unchanged |

The earlier selected Go/TypeScript/Python source inspection found no benchd
adapter in the latter two checkouts. That does not prove exhaustive absence or
what their independent tests cover. Their conformance to this proposal is unknown;
their tests have not been run for this change. No event catalogue, token audience,
Organization identity or network authorization is changed. Unix peer credentials
remain the operator authority; `initialize` cannot widen it.

## Behavior and migration

`schemas/superbench/operation.v1.json` adds only `initialize: true` to create/recover.
The CLI serialization explicitly sets it. Service dispatch uses warm creation
only for that explicit value. Omission still calls cold registration; changing
this default would silently alter old clients and is deliberately avoided.

| Client/service combination | Required behavior | Evidence required |
| --- | --- | --- |
| Old client, new service | Omitted field registers cold; no repository setup | Real daemon/Git compatibility drill |
| New client, old service | Reject unknown field before checkout effects; no cold fallback | Published pre-initialize daemon drill |
| New client, new service | Atomic reservation through setup; clean success checkpoints/releases | Canonical runtime creation cases |
| Invalid explicit field | False/null/wrong type, bypass flags and unrelated operations rejected | Canonical negative request vectors and live flag checks |
| Retry after completion | Same result; no repeated hooks | Durable-receipt replay assertions |
| Retry after interruption | `operation_in_doubt`; no redispatch after manual reconciliation | SIGKILL, fence/release, restart drill |

Warm success grants readiness for a subsequent acquisition, not permission to
write. Setup failure returns `422 operation_failed`; clean-state conflicts such
as untracked setup output retain the existing `409 conflict` mapping. The held
token is disclosed only in the authenticated operation result, never status.
These results require callers to check `ok` before using the checkout.

## Fixture ownership and checks

Canonical requests live in treaty's `fixtures/superbench-api/operations.json`;
creation lifecycle expectations live in `creation.json`. TypeScript validates
their request shapes and consistency. It does not run a lifecycle producer.
`scripts/sync-contracts.py` pins committed canonical bytes and hashes at the
treaty revision above; `testdata/source.json` records all three files.

The producer drill `scripts/test-creation.py` reads the creation corpus directly
from its pinned copy (or an explicit candidate path during development). It binds
only the abstract repository URL to a disposable fixture. Scenario labels select
committed hook/lockfile state; status, ownership, setup exit, history, receipt,
remote branch, dirty preservation and replay are checked against real output.
The old-service test must name the published pre-initialize binary. Consumer
coverage is not inferred from these producer tests.

Executed on a disposable Linux verification host: treaty formatting, no-emit
typecheck, lint, full Bun suite with private loopback NATS 2.15.2 (812 tests,
1,494 assertions), all package builds, and the Node 24 docs build (57 pages).
SuperBench required formatting, installer tests, vet, race tests and binary builds
passed. The actual daemon ran all eight canonical scenarios, mixed-service and
CLI defaults, and SIGKILL/restart replay checks. Setup/acquisition regressions,
unprivileged systemd preparation and XFS storage drills also passed, including
frozen-lockfile creation without hooks. Native macOS core, protocol and
architecture test binaries also passed (cross-compiled to avoid a local build
cache). See
[creation verification](creation-linux-output.txt) for raw results. This is
producer verification, not live suite integration or contract acceptance.

Publish the canonical proposal before the producer pin. Link both PRs, retain
the proposed status, and review/accept the ownership and wire contracts before
an opt-in AgentPod/SuperPipeline pilot. No production deployment or release tags
are authorized by this checkpoint.
