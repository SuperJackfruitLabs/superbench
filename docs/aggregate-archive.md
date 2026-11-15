# Aggregate archive and reviewed reap

Aggregate archival binds one workspace identity and its complete sealed member
vector. It preserves the scalar identity fields as empty and uses schema 18
workspace metadata for archive and reap progress. Public v2 CLI/service exposure
remains separate work.

Manual archive requires a ready, unleased workspace with no pending checkpoint
or stage. Under the host lock it verifies each registered clean checkout, its
HEAD, mirror identity, worktree registration and administrative lock. Independent
remote confirmation covers the exact seal, manifest digest, every immutable pin
and every current task ref.

A complete tree snapshot and permission intent are persisted before making the
root and member files read-only. Each member's `.git` control file is included;
the shared mirror is outside the frozen tree. An interrupted transition retains
the same intent for explicit retry. Archived workspaces cannot acquire owners,
prepare, execute or checkpoint. Completed creation/recovery replay returns the
retained inventory without recreating initialization ownership.

Automatic archive requires committed `archive.when = "branch-merged"` policy
for every member and matching physical policy bytes. At least one member must
have changed from its initial source. Each changed member needs a prior unmerged
observation of that exact checkpoint, configuration and remote target branch.
Every member must now be an ancestor of its observed remote default branch;
unchanged members may remain at their integrated initial source. One unmerged,
missing, moved or differently targeted member blocks the whole transition.
Genesis reachability alone never signals completion. Automatic archival never
releases a writer or deletes files.

Reap planning produces a concrete `superbench.workspace-reap.v1` manifest bound
to the workspace ID, member identities, seal/vector/digest, freeze digest, grace
deadline and complete filesystem snapshot. Recoverability is determined by each
member's exact Git blob bytes. Control files, ignored outputs and other unmatched
bytes remain explicitly non-recoverable and require review with the plan.

Deletion requires the same reviewed manifest after the grace deadline. Its
durable intent precedes deletion. Retry permits only missing approved entries
or unchanged remaining entries; replacement roots or new files are refused.
Remote evidence is rechecked without requiring surviving checkout paths. Only
the exact absent member worktree registrations are removed; no pruning or ref
deletion is performed. The single quota project is retired only after the root
is empty. Retained identities, allocation metadata, source provenance, sealed
checkpoints, logs and request receipts survive reap.

Recovery may use a retained remote seal after archive or reap, creating a fresh
identity and distinct task refs. Original task-ref ownership transfer still
requires explicit stopped-writer and worktree reconciliation.
