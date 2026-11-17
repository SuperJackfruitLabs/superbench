---
title: Errors
description: The closed set of error codes the local API returns, the HTTP status each comes with, and what to do about each one.
status: wet paint
---

A failed response has `"ok": false` and an `error` object with a `code` from the closed set below
and a human-readable `message` of at most 256 bytes. Each code comes with a fixed set of HTTP
statuses; a client that sees any other pairing should treat the response as invalid. This table is
checked against `internal/protocolv2` when the docs build.

| Code | HTTP status | Meaning | What to do |
| --- | --- | --- | --- |
| `invalid_request` | 400, 405, 413, 415 | The request was malformed: not POST, not JSON, too large, an unknown or missing field, a bad value. | Fix the request. Nothing ran. |
| `forbidden` | 403 | The caller's uid is not an operator (or a reader, for read-only operations). | Use an authorized account. |
| `not_found` | 404 | Unknown endpoint, or no such bench or workspace. | Check the path and key. |
| `conflict` | 409 | The current state refuses the operation, or a request ID was reused with a different body. | Read `status`; reuse an ID only with its original body. |
| `in_progress` | 409 | The same request is still running. | Wait, then retry with the same ID and body. |
| `operation_in_doubt` | 409, 503 | The daemon cannot tell whether an earlier attempt took effect, for example after a restart. It is never run again automatically. | Reconcile by hand: inspect status, stop the recorded execution, checkpoint and release. A new ID is not reconciliation. |
| `unavailable` | 503 | The service is shutting down, at its connection or active-operation limit, its receipt budget is exhausted, or a complete result would not fit. | Retry later with the same request ID, or query one key. |
| `operation_failed` | 422 | The operation ran and failed, for example a nonzero command exit or a refused checkpoint. A retained grant may come with it. | Inspect the retained workspace state. |
| `capacity_exhausted` | 422 | A fixed quota reservation cannot fit. The v2 refusal reports the needed and total byte and inode budgets. Only `/v2/operations` returns it. | Free capacity or request less. Limits are never raised to fit. |

When the receipt store is full, new growth operations are refused, but `stop`, `checkpoint`,
`release` and `archive` stay available so work can always be reconciled.

## The CLI

The `bench` CLI prints the response envelope and exits nonzero on an error. For `run`, a nonzero
command exit status propagates through the CLI once fencing completed. Without `--socket`, direct
local commands print the engine's own JSON and report errors on stderr as `bench: <message>`.
