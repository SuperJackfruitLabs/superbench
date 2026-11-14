# Prototype checks

CI runs scripts/check.sh with Go 1.26.4. go vet has a zero-warning baseline: any
new diagnostic fails. gofmt must report no files. No dependency is grandfathered.

Architecture tests cap Go files at 1000 lines and packages at 4000 lines, prohibit
external engine dependencies and reverse engine-to-CLI imports. Go rejects import
cycles at compile time. The CLI may depend on the engine; the engine uses only the
standard library. Filesystem/git integration tests cover leases, dirty state,
remote checkpoint rejection, handoff and recovery. The race detector runs locally.

The state implementation is deliberately provisional. Before benchd, replace it
with transactional storage and add recovery journals for filesystem operations,
process fencing, identity and API conformance tests. None is claimed by this scaffold.
