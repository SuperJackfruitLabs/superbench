# Changelog

Changes people will notice, newest first. Add an entry under `## Unreleased` with each such
change; before a tag, a release-prep PR runs `sh .github/release/prep-release.sh vX.Y.Z`, which
stamps the section `## vX.Y.Z — YYYY-MM-DD`, and the release notes quote it.

## Unreleased

- First release, v0.0.1. SuperBench is an experimental single-host prototype, not a production
  system, and nothing here has been proven beyond what the repository's docs describe. The
  release carries the four commands the managed installer needs: `bench`, `benchd`,
  `bench-quota` and `bench-payload`, built for Linux amd64 with CGO off, so every
  binary is static (`bench-payload` must be). Each archive is reproducible from the tag, and
  `SHA256SUMS` and a build-provenance attestation come with them. `bench version` and
  `benchd version` print the release version; a build from a branch prints `dev`. Read
  `docs/pilot-installation.md` before installing anything.
- benchd reads the default-off v2 switches (Fresh payload layout, finite I/O, receipt lookup,
  cancellation, registration observation) only from the root-owned
  `/etc/superbench/activation.json`, and logs them at start. With the file absent nothing
  changes. An activated daemon refuses state that a start without the switches has not migrated,
  and refuses a combination that would strand workspaces: `fresh_payload` needs `cancellation`
  and the installed service identity, and `finite_io` and `registration_observation` need
  `fresh_payload`.
  The managed installer creates the separate `superbench-payload` account and, with
  `--identity`, writes the payload policy and service identity the helper verifies; with
  `--activation` it writes the activation file with every switch off.
- Return a specific v2 logical-capacity refusal with needed and total byte/inode budgets when a fixed quota reservation cannot fit. Preserve quota limits, retained creation intents and nonzero failures; log only the same bounded diagnostics. Keep the v1 closed error code and message unchanged.

- Add a proposed explicit finite-I/O request codec with bounded opaque bytes and
  immutable digest vectors; keep legacy request encodings and service entrypoints unchanged.

- Add fixed-policy XFS allocation, host admission and shared store caps, frozen
  pnpm/npm setup, atomic mirror publication and bounded/reserved run logs.

- Add Linux cgroup-backed run/stop commands, retained leases on supervisor failure,
  local administrative fencing and schema-2 protection for execution metadata.

- Replace provisional JSON state with versioned SQLite, one-time legacy import,
  rollback-safe lease updates and read-only status.

- Persist creation intent before worktree changes; retry safely registers an exact
  journaled checkout after interruption and refuses dirty or conflicting files.

- Add a local experimental CLI for task worktrees, persistent cooperative leases,
  remote-confirmed checkpoints, warm handoffs and recovery into fresh host state.
