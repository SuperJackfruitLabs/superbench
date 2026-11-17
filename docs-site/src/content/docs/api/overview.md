---
title: The local API
description: benchd serves JSON over one host-local Unix socket, authorized by kernel peer credentials, with durable request receipts. No TCP listener.
status: wet paint
---

`benchd` owns one host-local Unix socket and one private state store, and serves the same
operations as the `bench` CLI over HTTP on that socket. The `bench` CLI is its only implemented
client: pass `--socket` to any operation. There is no TCP listener (read from the source; no test
asserts it, see the [Security model](/start/security-model/)). Never expose the socket through a
TCP proxy.

The request and response shapes are **proposed** contract data, pinned from the suite's contract
repository. They are implemented and tested here; no suite product consumes them yet, and a shared
response schema and published SDK for v1 are still pending.

## Endpoints

| Method | Path | What it is |
| --- | --- | --- |
| `GET` | `/healthz` | A health check, open to any authorized peer (operator or reader). Returns the service name and API version. |
| `POST` | `/v1/operations` | Scalar (single-repository) operations, version `superbench.v1`. |
| `POST` | `/v2/operations` | Grouped workspace operations, version `superbench.v2`. See [v2 operations](/api/operations/). |

Operation requests are `POST` with `Content-Type: application/json`; the health check is a `GET`. Three further v2 extensions exist in source but are
proposed and **disabled** in the public `Serve` entry point: cancellation (an `operation` of
`cancel` on `/v2/operations`), `POST /v2/receipts/lookup` for historical receipt evidence, and
`POST /v2/registrations/observe`. No request can turn them on.

## Who may call

The daemon reads the caller's uid from the kernel (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on
macOS); supplied identity headers grant nothing.

- **Operators** (`--operator-uids`, by default the daemon's uid and root) may run every operation.
  They are trusted to execute arbitrary commands as the daemon user.
- **Readers** (`--reader-uids`) may run `status` and `host_status` only.
- Anyone else gets `forbidden`.

The uid lists do not grant filesystem access by themselves: the socket is mode 0660 and its parent
must not be group- or world-writable, so arrange a trusted local group. Lease tokens are holder
capabilities, separate from the caller's uid.

## Request IDs and receipts

Growth operations (`create`, `recover`, `lease`, `run`, `setup`) require a `request_id`. The CLI
generates one and prints it to stderr when `--request-id` is omitted; save it before issuing work
whose reply might be lost.

- Retry with the **same ID and the same body**. The same ID with a different body is a `conflict`.
- IDs and responses are scoped to the authenticated uid.
- A completed reply is cached durably and replayed exactly, including a historical lease token. A
  replayed token does not restore an ended lease; use `status` for the current state.
- A retry while the operation is still running gets `in_progress`. A disconnected client does not
  cancel the work.
- After a daemon restart, unfinished receipts become `operation_in_doubt` and are **never run
  again automatically**. Inspect status, stop the recorded execution if needed, checkpoint and
  release with the retained capability. Changing the ID is not reconciliation.

Checkpoint, release and stop do not create growth receipts, so a full receipt store never blocks
reconciliation. Receipts are not collected; do not delete them to retry uncertain work.

## Shutdown

SIGTERM cancels supervised runs, which fence before keeping their lease for inspection. SIGKILL can
leave descendants alive; restart and `stop --admin` recover them from the durable execution scope.
HTTP shutdown has a ten-second grace period, which does not release ownership of an unfinished
operation. No timeout proves that a writer stopped.

## Bounds

Requests are at most 64 KiB. Every v1 response is at most 1 MiB; on v2, read-only responses are at
most 1 MiB and durable growth responses at most 64 KiB. Unknown fields, duplicate keys, null optional values, trailing documents and invalid
UTF-8 are refused. At most 64 concurrent connections, and 8 active operations by default
(`--max-active`, 1 to 64). An overlarge status fails; query one key. There is no pagination.
