---
title: v2 operations
description: Every superbench.v2 operation on POST /v2/operations, its required and optional fields, and the response envelope, as internal/protocolv2 defines them.
status: wet paint
---

Every v2 request is one JSON object posted to `/v2/operations` with `"version": "superbench.v2"`
and an `operation`. The fields each operation requires and accepts are fixed; any other field is
refused. These tables are checked against `internal/protocolv2` when the docs build.

```json
{
  "version": "superbench.v2",
  "operation": "lease",
  "key": "task-1",
  "holder": "run-1",
  "request_id": "lease-task-1"
}
```

## Operations

Besides `version` and `operation`:

| Operation | Required | Optional | What it does |
| --- | --- | --- | --- |
| `create` | `key`, `repositories`, `checkpoint_repository`, `initialize`, `request_id` | `quota_bytes` | Create a workspace from its member vector, prepare every member and confirm its genesis checkpoint. On a key whose workspace is `reclaimed`, a new `request_id` creates a new workspace there, with a new `bench_id`. So it does on a key whose create failed (`materializing` or `initializing`) once the host operator has retired that create's storage; until then it is a conflict. |
| `recover` | `key`, `repositories`, `source`, `initialize`, `request_id` | `quota_bytes` | Rebuild a fresh workspace with distinct task refs from an exact historical seal. |
| `status` | (none) | `key` | Read one workspace, or the whole inventory. Read-only. |
| `host_status` | (none) | (none) | Read the host's storage and admission state. Read-only. |
| `lease` | `key`, `holder`, `request_id` | (none) | Grant the workspace to one holder and prepare it. |
| `run` | `key`, `holder`, `arguments`, `request_id` | `checkpoint`, `repository_name`, `lease_token` | Run one supervised command in a member or the workspace root. |
| `setup` | `key`, `holder`, `request_id` | `offline`, `repository_name`, `lease_token` | Prepare one member, or every member. |
| `checkpoint` | `key`, `lease_token` | (none) | Checkpoint the complete member vector. |
| `release` | `key`, `lease_token` | (none) | Release the workspace after a complete checkpoint. |
| `stop` | `key`, `lease_token` | (none) | Fence the holder's supervised execution. |
| `stop_admin` | `key` | (none) | Operator stop without the holder's token; returns the retained capability only after fencing succeeds. |
| `archive` | `key` | (none) | Freeze the whole workspace read-only. Never approves deletion. |
| `reclaim` | `key` | (none) | Delete a released Fresh workspace's tree and retire its quota, ending at phase `reclaimed`. A legacy workspace is refused. With `fresh_payload` on, the daemon also reclaims a Fresh workspace idle past `fresh_reclaim_idle_hours` (default 24) by itself. |

`create`, `recover`, `lease`, `run` and `setup` are growth operations: they need a `request_id`
and get durable receipts. `status` and `host_status` are open to readers; everything else needs an
operator. See [The local API](/api/overview/).

## Fields

| Field | Shape |
| --- | --- |
| `key` | Letters, digits, `_` and `-`, starting with a letter or digit, at most 80 characters. |
| `repositories` | 1 to 32 objects: `name` (lowercase letters, digits, `_` and `-`, starting with a letter or digit, at most 64 characters), `repository`, and optional `branch` and `base`. |
| `checkpoint_repository` | The `name` of one member. |
| `source` | For `recover`: `repository`, `bench_id`, `commit` and `manifest_sha256` of the seal to rebuild from. |
| `initialize` | Must be `true`; warm initialization is mandatory. |
| `holder` | Letters, digits and `_ . : / -`, starting with a letter or digit, at most 200 characters. |
| `lease_token` | The holder's token: 32 or 48 lowercase hex characters. |
| `arguments` | 1 to 128 strings, each at most 8192 bytes; the first is not empty. |
| `checkpoint` | For `run`: omitted or `true` checkpoints and releases after success; `false` keeps the lease. |
| `repository_name` | The member to run or set up. Omitted, `run` uses the workspace root and `setup` prepares every member. |
| `offline` | For `setup`: offline frozen setup. |
| `quota_bytes` | One total workspace budget, within host policy: 1024 to 2^50 bytes, a multiple of 1024. |

## The response envelope

```json
{
  "version": "superbench.v2",
  "operation": "lease",
  "request_id": "lease-task-1",
  "ok": true,
  "result": { "workspace": { "key": "task-1", "phase": "ready" }, "grant": { "lease_token": "…" } }
}
```

A failed response has `"ok": false` and an `error` with a `code` and a `message` of at most 256
bytes; see [Errors](/api/errors/). Results are closed shapes:

- **Mutation**: the `workspace` (key, bench ID, phase, member count, checkpoint repository,
  `ready_for_acquisition`, quota bytes and inodes, and when present the lease summary, the latest
  checkpoint, a pending generation, the recovery source and the archive record), an optional
  `grant` with a `lease_token`, and an optional `root_path`.
  A workspace's `phase` is one of `materializing`, `initializing`, `prepared`, `ready`,
  `archived`, `reaping`, `reaped`, `reclaiming` or `reclaimed`; while it is being initialized,
  its lease holder is `initialization`. `reclaiming` never returns to `ready`: repeat the same
  `reclaim`. A `reclaimed` workspace stays `reclaimed` in `status` until a `create` on its key
  records the next one, whose `bench_id` is always new: bind a lease or receipt to the
  `bench_id`, not the key alone.
- **Run outcome**: a mutation plus the `execution` (ID and a phase of `starting`, `running` or
  `stopped`), `exit_code` and `log_truncated`.
- **Setup outcome**: a run outcome plus a `preparation` status for each member.

Responses omit repository URLs, private member paths, command arguments and environment, raw
manifests, continuation secrets and history. Status never discloses lease tokens. An outgoing
original-ref transfer adds an optional `transfer_state` of `relinquishing` or `transferred`.

## v1

The scalar API on `/v1/operations` (`"version": "superbench.v1"`) has the same operations for a
single-repository bench, with `repository`, `branch` and `base` in place of the member vector, and
no `archive`. `initialize: true` on `create` and `recover` asks for warm creation; the current CLI
always sends it, and an older service rejects the unknown field rather than registering cold.
