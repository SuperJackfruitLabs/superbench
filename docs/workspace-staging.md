# Durable workspace staging

`Store.StageWorkspaceContext` is the internal aggregate creation operation. It
accepts a creation identity and the complete repository specification. The
public CLI and host service still expose the scalar v1 lifecycle.

The operation retains the host state/mirror lock across sequential commits:

1. Migrate and inspect the existing key. Refuse a scalar record, another creation
   identity, changed specification, existing unregistered root or scalar journal.
2. Select every source commit and quota declaration. Persist the complete vector,
   stable workspace identity and initialization lease before allocating the bench.
3. Obtain one total byte/inode allocation for the workspace and persist its
   project identity before creating any member checkout.
4. Materialize the pinned vector, checking durable ownership before each member.
   Commit `initializing` after every checkout succeeds.

A retry of a partial operation uses its stored sources and original owner.
It preserves completed checkouts and refuses dirty or conflicting files. It
does not refresh the plan to newer remote tips. A failed allocation-state save
returns the previously committed record; retrying can retrieve the helper's
existing logical allocation. An already staged identical request repeats no
checkout or allocation effects.

Schema 14 distinguishes these records from scalar benches. Older binaries must
refuse this schema. State updates reject changes to aggregate identity, member
sources, initialization token and assigned allocation. Scalar status refuses
aggregate projections, scalar mutations reject them, and scalar automatic
archive skips them. Service receipt reservation checks the record kind while
holding the shared state lock.

Staging leaves the initialization lease held. It does not run preparation,
publish a checkpoint, grant a stage writer, release ownership or declare the
workspace ready. Those lifecycle transitions and v2 service/CLI responses remain
required work. The returned internal lease token must not appear in public
status; `Snapshot` redacts it.

Cancellation bounds Git/source/materialization work and is checked between
commits. Existing blocking host-lock waits and quota-helper execution are not
interrupted by this context.

See the [verification evidence](workspace-staging-output.txt) for native/Linux
checks, the real shared XFS quota drill and remaining capability limits.
