# Reviewed original task-ref transfer

Distinct-ref recovery remains a fork of a historical seal. Original task-ref
transfer is a separate local administration flow: permanently retire the old
workspace's writer authority, preserve its files, and initialize a fresh workspace
on the same repository branches. The current implementation requires source and
destination in the same host state/data pair. Cross-host transfer, an unreachable
source and return quarantine require additional authority and verification.

```sh
# First stop/fence any registered writer, reconcile unknown history if needed,
# checkpoint the complete current vector and release the source owner.
bench workspace inspect-transfer --state /private/state --data /workspace/data \
  --target restored --transfer-id move-1 original > transfer-plan.json
# Review every source/target identity, branch, integration base and snapshot.
bench workspace begin-transfer --state /private/state --data /workspace/data \
  --plan transfer-plan.json --approve DIGEST original
bench workspace continue-transfer --state /private/state --data /workspace/data \
  --transfer-id move-1 original
```

Use the actual configured storage and cgroup-root settings. These commands are
local-only; a socket option and ambient lease token cannot confer this authority.
Inspection accepts an optional explicit destination `--quota-bytes` request,
subject to the same repository declarations and fixed host ceiling as creation.
The plan contains no lease token or command/environment inputs. Protect any
returned destination grant as a capability.

The source must be ready, unleased and unarchived, with no pending generation or
running member. Finish its exact pending publication first. Every current HEAD,
local task tip, remote task tip and complete checkpoint must agree. A held writer,
dirty or unpushed work, unknown registration, lock, changed mirror or stale plan
refuses transfer and preserves files. This is trusted-caller coordination, not
protection from arbitrary filesystem writers or a claim of UID isolation.

Approval binds the private source-row digest, fresh destination ID/key/creation
identity, exact quota selection and whole repository/checkpoint vector. Each
member snapshot binds physical checkout/common/admin identities, fixed Git
metadata, index and all checkout entries including ignored artifacts. A compact
streaming digest covers raw filename/symlink bytes, identities, modes and content;
current safety ceilings are 1 TiB and one million entries per member. Oversize or
changed evidence refuses before adoption. No cache files are silently excluded.

Begin commits the immutable approval and a source `relinquishing` marker before
any branch effect. It is an irreversible launch prohibition, including after a
crash or destination failure. The source's ordinary initialization phase remains
historical inventory; it does not establish availability for acquisition.
Original create/recover requests cannot resume the source or obtain a new grant.

Continue revalidates the approval under source exclusion and the host lock. It
detaches only each exact source worktree's HEAD at its existing commit, leaving
the index, tracked/untracked/ignored files and registration intact. Locked or
replaced worktrees refuse. An interrupted partial detachment retries only the
same approved observations; there is no reset, prune, forced checkout or automatic
rollback. Changed evidence stays preserved for operator reconciliation.

After the whole source relinquishes ownership, the destination receives its
approved fresh identity, allocation and initialization owner. It uses the original
task refs and integration bases, exact sealed commits and source provenance, with
fresh setup receipts and a newly confirmed genesis. Setup or publication failure
retains that destination and its evidence. A stopped destination needs explicit
initialization repair; old transfer continuation cannot adopt its repair grant.
Journal completion follows ready/unleased confirmation. Lost replies replay
idempotently; later ordinary owners never leak their grants through this flow.

The old source can subsequently archive/reap through its existing reviewed flow.
It proves its immutable seal/pins and exact detached registration, allowing the
destination to advance the shared task branches. Read-only and deletion intents
still bind the source's actual files. Registration removal and quota retirement
affect only that approved source; destination refs, files and allocations remain.
Ordinary archive/checkpoint/release retain their current-task-tip checks. Retained
ordinary schema-20 reap approvals keep their prior exact retry semantics.

Schema 21 prevents old binaries from ignoring transfer authority and preserves
history, initialization-repair journals and service receipts. Stop older active
supervisors with their installed binary before upgrading. Historical HTTP receipt
bytes remain unchanged; collection never makes a creation request executable again.

Native and Linux required checks and the managed same-host transfer drill passed.
The drill includes a surviving detached writer after daemon crash, explicit fencing
and unknown-history review, partial member detachment, fresh destination setup and
quota, advancement of the original refs, and reviewed retirement of only the old
source allocation/registrations. See [verification evidence](workspace-transfer-output.txt)
for the exact frozen artifacts, regression results and uncovered cases.
This local flow does not add a public/network treaty operation or establish suite
consumer acceptance. Phase 1 and the full implementation goal remain open.
