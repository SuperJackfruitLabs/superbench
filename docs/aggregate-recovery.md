# Recovery from a sealed aggregate checkpoint

The internal store can rebuild a multi-repository workspace from an explicitly
selected historical checkpoint seal. `RecoverWorkspaceContext` creates a fresh
identity and initialization owner, prepares each member, and confirms a new
genesis checkpoint before releasing initialization ownership. The public CLI
and service still expose v1.

The request supplies the source metadata repository, source workspace ID, exact
seal commit, raw manifest SHA256, and complete repository allowlist. Recovery
confirms the seal belongs to that workspace's metadata chain, checks its exact
bytes, and confirms every immutable member pin. The sealed member names and
repository identities must match the caller's complete allowlist. Manifest
contents cannot introduce another remote. Current task or base tips never select
recovery sources.

Source quota declarations are read at those exact commits and reconciled into
one total allocation. The complete vector, provenance, new identity and new owner
are committed before quota assignment or checkout materialization. A later
checkout or state failure preserves the committed vector and owner for retry;
retry does not follow a moving remote branch or reinterpret the original seal.
Occupied lookup keys, unknown directories and legacy creation journals refuse
replacement. Unknown files are preserved.

Schema 17 stores immutable `RecoveredFrom` provenance. Recovery copies no old
lease, continuation, execution, setup receipt or quota project. Member setup uses
the new identity's receipts and the host's existing shared caches. Success creates
new pins and metadata under the new workspace ID, with an empty predecessor;
the source seal remains provenance rather than becoming a parent in another
identity's checkpoint chain. Completed replay returns inventory and cannot grant
a later holder's capability.

Cancellation during warm finalization preserves the committed owner in the
result and revokes automatic continuation before returning. A stopped or
cancelled initialization still requires explicit administrative reconciliation;
it does not authorize replacement, automatic retry or lease expiry.

This path uses distinct destination task refs. It can recover an older complete
seal while the source has a live writer, newer commits or a partially published
checkpoint, because it neither moves source refs nor transfers source ownership.
Reusing original task refs requires separate proof that their prior writer has
stopped or been fenced, and reconciliation of any registered worktree ownership.
That transfer path remains pending. Recovery never treats a missing heartbeat or
elapsed lease time as proof of writer termination.
Aggregate creation and recovery also refuse task branches that occupy the
private checkpoint metadata namespace or its ancestors; reading a source there
does not grant ownership of that namespace.

Fresh-workspace recovery is separate from resuming an interrupted initialization
or repairing an existing checkout. Aggregate archive/reap, original-ref ownership
transfer, public v2 responses/CLI, native supervised laptop preparation and the
opt-in board pilot remain outside this internal checkpoint.
