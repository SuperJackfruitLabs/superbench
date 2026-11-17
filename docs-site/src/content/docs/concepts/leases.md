---
title: Leases
description: A lease grants a bench to one holder at a time. Leases are cooperative, they survive restarts, and they never expire on their own.
status: wet paint
---

A **lease** grants a bench to one holder. While it is held, other `bench` acquisitions of that
bench are refused.

```sh
bench lease --holder first-agent fix-login
```

The response includes `Lease.Token`, the holder's capability, and `Path`, the checkout. The token
is returned only on acquisition, on an administrative stop (`stop --admin`) or on exact replay of a
completed request's receipt; status and checkpoint output redact it. Keep it in
`BENCH_LEASE_TOKEN` rather than passing `--token`, which shows it in process arguments.

## Leases are cooperative

This is the caveat that travels with every lease claim:

- A lease stops other **`bench` acquisitions**. It does not stop direct edits, an arbitrary shell,
  or other processes running as the same user.
- There is **no automatic expiry**. A crashed holder's lease is kept until someone inspects it.
  Time passing never ends a lease or reassigns a process.
- It covers **one host**. Moving a bench between hosts is a reviewed administrative procedure, not
  network coordination.

The repository's tests cover one winner among twelve simultaneous lease requests, a lease that
survives a restart, refusal of a wrong or old token, and a second holder finding the first holder's
files. Its recorded handoff shows files moving between holders; it does not show two agent
harnesses working.

## Holding, losing and recovering a lease

- **A process crash keeps the lease.** Stop and inspect the holder before using its saved token to
  checkpoint or release.
- **A failed checkpoint keeps the lease.** A rejected push, a dirty tree or an ambiguous state save
  leaves it held.
- **A lost supervised token** is recovered with `bench stop --admin`: a local operator fences the
  recorded scope and gets the retained lease and token back. Cooperative shell leases still need
  manual inspection, because nothing fenced them.
- **Releasing twice** can fail after the first release cleared the lease. Check `status` rather than
  applying an old token to a new holder.

## Repair leases

`bench lease-repair` is a local administrative lease for maintenance. It skips the mirror refresh
that ordinary acquisition performs, so it stays available when the remote is not. Ordinary `lease`,
`run` and `setup` refresh the shared mirror first and refuse if that fails.

## Leases through the daemon

Over the socket, `lease` is a growth operation and needs a request ID. Repeating a completed lease
request returns its historical reply, including its token, without changing the current holder. A
replayed token does not restore an ended lease; always use `status` to find the current state. See
[The local API](/api/overview/).
