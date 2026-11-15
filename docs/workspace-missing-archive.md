# Archiving a transferred source with missing checkouts

After reviewed missing-checkout transfer has completed, the original source can
be archived and reaped without reconstructing absent files. The destination must
have reached ready and released ownership at least once. The source remains
unavailable for acquisition throughout this lifecycle.

Archive records whether the source root is present or absent, its fixed parent
identity, the original transfer approval, retained Git evidence and the immutable
remote checkpoint seal. An absent root is not represented as an empty tree.
Actual present worktree bytes, including dirty and ignored files, enter the
reviewed tree. Private index and reflog object retention separately protects
index-only staged objects that were never committed. Missing-checkout quarantine, history and request receipts remain outside
the deletion authority.

Use the existing local `bench workspace archive`, `plan-reap` and
`reap --plan FILE --approve DIGEST` commands. Review the complete plan, including
unrecoverable files, before approving deletion. The persisted archive grace still
applies. A root-only directory returned before archive can be observed under fresh
archive authority; a returned registered missing checkout is refused. An explicitly absent
root returning after freeze is refused; a present frozen root must retain its
approved identity and entries. After root-removal intent, any present root
requires fresh review.

Managed quota retirement independently verifies the original allocation, mount,
caps and fixed parent. The helper requires the source root to be absent and XFS
to report zero blocks and inodes, including open unlinked files. It never removes
directories, changes permissions or recursively deletes data. A durable tombstone
preserves the retired allocation identity. An interrupted final state commit can
resume against that exact retirement without granting the allocation again.

## Explicit disposition after interrupted root removal

The original reap records root-removal intent before unlinking its empty root.
If a directory is present after interruption, matching inode and mode do not prove
that it is still the original directory. Ordinary retry preserves it. An operator
may separately inspect and approve removal of the **current empty directory**:

```sh
bench workspace inspect-root-removal --state /absolute/state \
  --data /absolute/data --disposition-id fresh-review source-key > root-review.json
# Review every binding and the remove-current-empty-directory action.
bench workspace reconcile-root-removal --state /absolute/state \
  --data /absolute/data --plan root-review.json --approve REVIEW_DIGEST source-key
# Resume the original reviewed reap after this acknowledgement.
```

These operations are local administration; they do not accept a socket or lease
token. Inspection does not mutate state. Approval binds the exact storage,
transfer, freeze, original reap, removal intent, observed directory and fixed
parent. Removal accepts only an empty physical directory and journals a unique
attempt before unlink. Once an attempt starts, a present directory requires a new
observation and approval. Exact replay may confirm absence only.

The acknowledgement reports `root-absent`; it grants no lease and makes no claim
that quota retirement or reap has completed. Resume the original reap to finish
those steps. This additional approval does not change the original plan or
release retained history or receipts.

## Limits

This is same-host source retirement after completed transfer, not cross-host
recovery or approval to clean unrelated host files. An external Git GC can rewrite
an original quarantined HEAD reflog. Strict evidence verification then refuses
replay even when private retained copies and object pins survive; GC-transparent
replay is not promised. Process-crash injection before root unlink is covered by
native tests; managed runtime verification must be read at its recorded scope.

The exact source and runtime checks are recorded in the
[verification evidence](workspace-missing-archive-output.txt).
