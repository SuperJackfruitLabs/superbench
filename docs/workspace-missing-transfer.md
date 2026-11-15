# Recovering original task refs after checkout loss

Missing-checkout recovery is a separate reviewed local operation. It recreates
the last remotely confirmed whole checkpoint in a fresh workspace on the original
task branches. It does not recover missing uncommitted files, certify unknown run
outcomes, or treat a missing directory as proof that a writer stopped.

Native/Linux required checks and managed recovery regressions
[passed](workspace-missing-transfer-output.txt). The current scope is one host with
the original private state, registered mirrors and authoritative storage ledger
still available. Missing-source archive/reap and cross-host recovery remain open.

## Stop and review

First administratively stop the source and verify its recorded execution scope
is empty. A surviving detached process after daemon failure must be fenced. If
the supervised outcome is unknown, use the existing explicit history inspection
and reconciliation flow. That observation retains an unknown exit result; do not
checkpoint or release the missing checkout to manufacture successful completion.

```sh
bench workspace inspect-missing-transfer --state /private/state \
  --data /workspace/data --target restored --transfer-id recovery-1 original \
  > missing-plan.json
# Review the complete identities, sealed vector, storage and retained evidence.
bench workspace begin-missing-transfer --state /private/state \
  --data /workspace/data --plan missing-plan.json --approve DIGEST original
bench workspace continue-missing-transfer --state /private/state \
  --data /workspace/data --transfer-id recovery-1 original
```

Use the configured storage/helper/cgroup settings. These are local administration
commands; socket flags, supplied lease capabilities and ambient tokens cannot
replace approval. A returned destination grant belongs only to that approved
fresh initialization epoch. Keep it private.

Inspection requires a complete checkpoint, no pending publication and at least
one genuinely absent member. Finish an existing pending generation through its
normal exact-vector path if possible. A task tip ahead of the selected seal is
preserved and refuses this operation; loss never authorizes a rewind or selecting
an arbitrary newer vector. Present member files, including ignored and staged
work, are bound and retained rather than copied into the recovered checkpoint.

## Storage and registration evidence

Managed storage verification reads the existing fixed policy, UUID-bound ledger,
ownership anchor, actual mount and source project limits. It never allocates,
creates an anchor or falls back to a parent filesystem when the data root is
absent. Unmanaged verification binds the existing physical data root and actual
kernel mount identity without claiming a managed UUID or quota. Unrelated target
allocations may coexist; the selected source assignment and immutable storage
authority must stay unchanged.

Each absent member binds the first absent path component beneath verified
physical parents. Losing the entire workspace root is one shared root absence,
not separate observations of its missing descendants. Symlinks, inaccessible
parents, changed mounts, I/O errors and replacement paths refuse inspection.

The exact linked-worktree administration, backlink, index, local branch and
sealed commit remain evidence. Unknown or duplicate registrations and preexisting
locks refuse. The complete member vector is preflighted before the first detach.
An approved missing registration receives private evidence and an exact owned
lock, then only its administrative HEAD is detached at the same commit. Its
administration remains in place. No prune, forced worktree removal, reset or
creation of the missing source path is part of recovery.

The retained evidence and object refs live inside the mirror's shared allocation.
Staged and reflog objects absent from the sealed tree remain locally rooted for
separate reconciliation. Current bounds are 64 MiB of administrative bytes,
100,000 metadata entries, 4,096 retention roots and a 16 MiB approval. Exceeding a
bound refuses rather than discarding evidence. Failed unpublished staging
artifacts remain preserved for reviewed collection.

## Irreversible retirement and fresh initialization

Begin atomically records approval, permanent source launch exclusion and the
exact stopped owner's retirement evidence. The source loses its live lease;
its execution, stage and run identities remain protected evidence. History rows,
unknown outcomes, preparation records and historical service receipts do not
change. Retirement does not associate lost work with the old checkpoint.

The destination gets a fresh identity, allocation, initialization owner, complete
preparation and remotely confirmed genesis using the original integration bases.
A stale or reappearing source stops progress without modifying returned files.
Source evidence is rechecked at destination preparation and readiness boundaries;
a failed check fences and revokes an existing initialization continuation.
Repair uses a separate reviewed fresh owner and cannot bypass source evidence.
Old continuation and command receipts never acquire the repair grant.

Schema 22 prevents older binaries from ignoring retirement evidence. Stop older
active supervisors with their installed binary before migration. Ready sources
with an outgoing transfer remain unavailable for acquisition; lifecycle phase
alone does not confer writer authority.

## Remaining acceptance

The verified scope and retained coverage limits are recorded in the
[verification evidence](workspace-missing-transfer-output.txt). Retained
missing-source registrations, history and shared evidence must not be removed
through ordinary cleanup.
Source archive/reap needs its own absent-member-aware approval and quota
retirement path. Cross-host takeover additionally needs independently verified
infrastructure fencing and return quarantine outside a restorable workspace
database. No suite consumer acceptance or completion of Phase 1 is claimed.
