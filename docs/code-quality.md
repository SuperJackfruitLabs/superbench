# Prototype checks

CI runs scripts/check.sh with Go 1.26.4. go vet has a zero-warning baseline: any
new diagnostic fails. gofmt must report no files. The SQLite driver is the only allowed external runtime dependency.

Architecture tests cap Go files at 1000 lines and packages at 4000 lines, allow the SQLite driver only in its storage adapter and prohibit other
external engine dependencies and reverse engine-to-CLI imports. Go rejects import
cycles at compile time. The CLI may depend on the engine; the engine uses only the
standard library plus the storage adapter's SQLite driver. Filesystem/git integration tests cover leases, dirty state,
remote checkpoint rejection, handoff and recovery. The race detector runs locally.

SQLite now provides transactional state storage. Before benchd, extend
recovery journals across all filesystem operations and add
process fencing, identity and API conformance tests. None is claimed by this scaffold.

SQLite storage uses modernc.org/sqlite v1.62.0 through database/sql. Its import is
allowed only in sqlite.go; the rest of the engine does not depend on driver APIs.
Version 1 creates the benches table and atomically imports the old JSON snapshot.
The JSON file is retained unchanged; later commands use SQLite exclusively.
Unknown schema versions fail closed. The driver and its transitives are recorded
in go.mod/go.sum; there is no floating dependency during CI.
