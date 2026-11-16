# Changelog

## Unreleased

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
