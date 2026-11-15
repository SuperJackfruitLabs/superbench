# Aggregate initialization and readiness

The internal `InitializeWorkspaceContext` path now connects durable staging,
member preparation and the complete genesis checkpoint. The public CLI and
service still expose v1; this is not a v2 producer contract.

One initialization token and private continuation cover every member. Setup
runs sequentially in registered member directories. Each launch has a durable
starting record and member/input identity before supervised process start.
The shared payload runner fences descendants, records stopped state and seals
the bounded log before success is persisted. Operator stop revokes automatic
continuation and prevents delayed supervisors from launching later stages.

Preparation fingerprints cover the parsed configuration, package.json and the
supported pnpm/npm lockfiles. Aggregate once receipts bind that full fingerprint.
Changing those inputs revokes the old member proof before cache or hook effects.
Successful members are skipped on initialization retry; a sealed successful
stage can finish a failed proof save without repeating hooks. Failed sealed
stages remain protected under the same lease alongside earlier successes.
Aggregate retention allows 64 stages, independently of the scalar limit of 8.

Explicit `PrepareWorkspaceContext` accepts a registered member selector; an empty
selector prepares every member. Configured hooks and frozen pnpm/npm installs
use the member's cache settings and its pinned source quota declaration, while
allocation remains one total workspace budget. A repository with neither hooks
nor a supported frozen lockfile requires no automatic setup command. Commands
currently require the Linux supervised execution backend; command-free Git
initialization also works natively. Native supervised preparation remains open.

Checkout identity is revalidated before member cache effects, immediately before
launch and before recording success. Earlier hooks cannot redirect a later
member to a symlink or unrelated checkout with matching input files. This
protects registered lifecycle paths; it does not provide filesystem isolation
from trusted commands or unknown external writers.

After all members have durable preparation proofs, the workspace is `prepared`
with its lease held. Checkpointing captures every clean HEAD and commits one
pending generation and digest before any push. Partial pushes or failed saves
retain the exact vector and owner. Retries cannot silently select newer HEADs.

Readiness requires an independent final check of preparation fingerprints,
stopped writers, clean checkouts, the complete seal and pins, and every current
remote task ref. Releasing retained history and removing the initialization
lease occur in the same transaction. A failed readiness save rolls both back.
Only that successful transition publishes `ready`.

Schema 15 supports these lifecycle records and migrates legitimate schema-14
staging records. It preserves scalar migration guards and rejects older-schema
records pretending to contain newer lifecycle fields. Scalar operations continue
to refuse aggregate workspaces.

Ordinary aggregate stage acquisition/execution/release, cold recovery into a new
workspace, aggregate archive/reap, v2 responses/CLI and the board pilot remain
required work. The initialized workspace path is not evidence that Phase 1 is
complete.

See the [verification evidence](aggregate-preparation-output.txt) for native and
Linux required checks, live XFS/cgroup drills and the exact verified artifacts.
