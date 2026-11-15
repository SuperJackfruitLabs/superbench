# Reviewed collection of unpublished mirror clones

New clones reserve a private staging directory through a durable ownership
journal before Git starts. Publication moves only the completed bare repository
into the registered mirror path. Failed or interrupted clones preserve staging
files; they are never recursively removed by the clone error path.

A managed Linux clone records its original Git control root. Local collection
holds the host lock and reconciles durable Git control intents before inspecting
or deleting files. Controller exit, elapsed time, a PID, and ordinary Git success
are insufficient fencing evidence. Cooperative clones and legacy unjournaled
staging directories remain protected. This does not implement shared mirror or
package-store garbage collection.

## Review and collect

Use the exact clone ID from the private state/mirror-stages journal. Flags must
precede the ID; this namespace is local administration and accepts no socket or
token flags. State and data must name the original host paths.

```sh
bench mirror-clones inspect --state /absolute/state --data /absolute/data CLONE_ID > collection.json
# Review every entry: collection discards these unpublished staging bytes.
bench mirror-clones collect --state /absolute/state --data /absolute/data \
  --plan collection.json --approve REVIEWED_DIGEST CLONE_ID
```

The plan binds the original private journal, parent and root identities, exact
entry paths, modes and content hashes, and original managed control root.
Approval is durable before deletion. Changed, additional, special or replaced
entries refuse collection; retries delete only unchanged remaining approved
entries. Symlink targets are preserved. Published mirrors, registered task
worktrees, refs, history and service receipts are outside this collection.

A durable root-removal intent precedes removing the empty staging root. Once
that intent exists, an old retry refuses any present root, even if its inode and
mode happen to match. A separately reviewed disposition can remove the current
empty root; it makes no claim that this is the original directory instance.

```sh
bench mirror-clones inspect-root-removal --state /absolute/state --data /absolute/data CLONE_ID > empty-root.json
bench mirror-clones reconcile-root-removal --state /absolute/state --data /absolute/data \
  --plan empty-root.json --approve FRESH_REVIEW_DIGEST CLONE_ID
```

A completed collection retains its immutable approval for exact result replay.
An inspect of such a collection returns that historical approval, not a fresh
snapshot. After reviewing completion, retire only its owned metadata explicitly:

```sh
bench mirror-clones forget --state /absolute/state --data /absolute/data \
  --approve ORIGINAL_COLLECTION_DIGEST CLONE_ID
```

Metadata retirement records its exact owned files before unlink and disables the
approval first. Interrupted retirement is retryable while unknown files remain
untouched. Old collection plans cannot recreate the retired ownership journal.
Internally generated clone IDs are distinct from external service request IDs;
this command never retires HTTP replay protection. Both journal directories have
bounded entry counts; no cap is raised to bypass review.

Normal managed publication automatically removes only its empty owned stage and
retires the associated metadata under the same host lock. A successful
cooperative clone retains its empty stage and ownership records because it lacks
managed descendant-fencing evidence.

## Verification boundary

Component race tests cover changed entries, interrupted deletion and metadata
retirement, returned roots, fresh single-use disposition, provenance checks and
journal budgets. scripts/test-mirror-clones.py is a disposable Linux runtime
drill: it kills an owned clone controller, proves a detached writer survives,
then requires managed inspection to fence it before collection. It also checks
managed publication, cooperative refusal, exact replay, outside-symlink target
preservation and root disposition. Runtime results must be recorded separately;
the existence of this driver is not evidence that it passed.

No cross-host transfer, rollback-resistant backup authority, cooperative-stage
adoption, shared-cache GC or whole Phase 1 completion is claimed here.
