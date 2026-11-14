# Changelog

## Unreleased

- Add Linux cgroup-backed run/stop commands, retained leases on supervisor failure,
  local administrative fencing and schema-2 protection for execution metadata.

- Replace provisional JSON state with versioned SQLite, one-time legacy import,
  rollback-safe lease updates and read-only status.

- Persist creation intent before worktree changes; retry safely registers an exact
  journaled checkout after interruption and refuses dirty or conflicting files.

- Add a local experimental CLI for task worktrees, persistent cooperative leases,
  remote-confirmed checkpoints, warm handoffs and recovery into fresh host state.
