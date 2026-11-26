---
title: Remote access
description: An opt-in SSH door onto one bench, for the lease holder only. Off by default, certificate-only, 30-minute certificates, sessions cut with the lease. Not isolation of untrusted code.
status: wet paint
---

Remote access lets the holder of a lease reach that one bench over SSH from another machine on the
fleet network. It is **off by default**, and while it is off `benchd` opens no TCP listener at all.
It is an experimental addition, built for trusted callers on trusted repositories, and it is **not
isolation of untrusted code**.

## Turning it on

Two settings in `/etc/superbench/activation.json` (see the pilot installation guide for the whole
file):

| Setting | Meaning |
| --- | --- |
| `remote_access` | `true` starts the fleet listener. It requires `fresh_payload`, and the file is refused without it. |
| `remote_fleet_address` | The one address the listener binds, as an IP and port in the string form `"ADDRESS:PORT"`. A wildcard address, a zero port or a zone is refused. It is required with `remote_access` and refused without it. |

With remote access on, `benchd` checks four requirements at start, before it opens the CA, the
host key or the listener, and **exits** if one is missing, naming it:

- a kernel with `mount_setattr` (Linux 5.12 or later), which `benchd` probes;
- a `/var/tmp` that is a directory, not a symlink;
- a `/dev/shm` that exists as a directory;
- a data root and a state root that are not `/tmp`, `/var/tmp` or `/dev/shm` and not under one of
  them, compared by path.

Each session mounts its own tmpfs over those three directories, which is why. The fleet address
does not have to exist when `benchd` starts: it starts and listens on that exact address even
before the tailnet brings it up, and accepts connections from the moment the address arrives. Any
other remote-access failure at start, such as an address already in use, also **stops `benchd`**:
it does not start without the door it was told to open.

The release that adds remote access also adds state schema 31, and an activated `benchd` never
migrates. So **every activated host must migrate on this upgrade, whether or not remote access will
be turned on**: with the unit stopped, run `bench migrate --state <state> --data <data>` as the
service account, then start it. Without that step an activated `benchd` refuses to start. The
upgrade is one-way once the new `benchd` has started on schema 31, unless the state backup taken
before it is restored. The pilot installation guide has the steps.

## Certificates

`benchd` keeps its own certificate authority. Nothing else signs a door certificate.

- A **writer certificate** lasts 30 minutes. Its principal is `b-<bench id>`, where the bench id is
  the workspace's id, and it carries the extension `superbench-lease@superbench.dev`, which names
  the lease ID and generation it was issued for.
- Only an operator on the Unix socket can ask for one, with `POST /v2/remote/credentials` or
  `bench workspace remote-credential`, presenting the lease token and the public key to certify.
  The caller keeps the private key. The key must be a bare `ssh-ed25519` or `ecdsa-sha2-nistp256`
  key, not one of `benchd`'s own; an ed25519 key that is not a valid point, or has small order
  (signatures under it can be forged without a private key), is refused.
- A **renewal** is the same request over the socket. It extends connections that are already open.
- A certificate stops working when its lease generation changes, when the lease ends, and after a
  restart of `benchd`: certificates are refused after a restart and must be asked for again.

```sh
bench workspace remote-credential --socket /run/superbench/benchd.sock \
  --public-key ./agent_ed25519.pub --certificate-out ./agent_ed25519-cert.pub fix-login
```

The reply also carries the address, the host key and its fingerprint, and `working_directory`, the
absolute path every session starts in. Pin the host key; do not accept it on first use.

## What a session can do

The door accepts certificates and nothing else: no passwords, no keys without a certificate. A
session may:

- run a command (`exec`);
- open a shell with a PTY;
- use SFTP, served by `bench-sftp`, an installed helper at `/usr/local/libexec/bench-sftp`.

```sh
ssh -i ./agent_ed25519 -o CertificateFile=./agent_ed25519-cert.pub \
  -o UserKnownHostsFile=./bench_known_hosts -p PORT b-<bench id>@ADDRESS 'git status'
```

Refused: port forwarding of every kind, agent forwarding, X11, and any subsystem other than SFTP.

Each session runs in its own private mount namespace that shows only its bench, a PID namespace
with a private `/proc`, an IPC namespace and private `/tmp`, `/var/tmp` and `/dev/shm`. A
seccomp filter denies creating user namespaces. This keeps one remote caller from wandering into
another bench by accident. It is a boundary for trusted callers; it is not isolation of untrusted
code.

## Sessions are the lease's remote execution

A live remote session is recorded as the lease's remote execution, the way a supervised run is its
local one. So `checkpoint`, `release`, `stop` and `stop_admin` fence the sessions first: they are
cut, and their leftover processes are killed, before the operation goes on. A holder cannot keep a
shell open across a release.

## Egress

A session has the same network access a supervised run has, and supervised runs have no network
isolation today. Remote access adds a way in; it does not add or remove any restriction on what the
bench can reach.

## Audit and status

`benchd` records each certificate issued and each session opened and closed: key, bench, source
address, the kind of session, the command name, bytes in and out, and why it ended.

```sh
bench workspace remote-status --socket /run/superbench/benchd.sock [key]
```

prints the listener address and host key fingerprint, the live remote executions, and those audit
rows, for one key or for all of them.

## Known limits

These are documented follow-ups, not built:

- A supervised run shares the host's process view, so it can still reach a live session's bench
  through `/proc` and ptrace. The reverse direction is closed; this one is not.
- Abstract Unix sockets and loopback networking are shared by processes of the same user.
- There is no per-bench connection cap, no per-source limit before authentication and no rate
  limiting. The listener serves the fleet address only; these are needed before a public one.
- Session limits are not configurable yet. A bench takes at most 8 sessions at once, a fixed
  number, and a session has no wall-clock limit: only the certificate's 30 minutes bounds a
  connection, and renewal extends it without bound.
- The host key fingerprint is printed by `bench workspace remote-status` and returned with each
  certificate, not by `bench host-status`.
- Renewal is a request on the Unix socket only; there is no renew request on the open SSH
  connection.

## Not built yet

Watchers with read-only access, the operator key list, OIDC authentication, the public listener, and
the harness wiring that asks for certificates automatically. A suite coordinator can now ask
through its node; the harness wiring ships in its own release. A caller needs socket access and
the lease token to obtain a certificate.
