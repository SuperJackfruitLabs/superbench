# Remote access to benches: design

Status: draft for operator review, 2026-11-24.

## Intent

An agent runs wherever it lives: a fleet VPS, a laptop, or (later) an outsider's machine. When a
card first needs a workspace it gets a bench on a bench host, and that bench belongs to the card.
Every agent on a later stage, wherever it runs, works in that same bench remotely until the card is
done. The bench host holds the files and runs the work; agents only send it their actions.

Today half of this exists: a bench is keyed per card, shared by every bench stage, and (AgentPod
v0.1.133) kept warm until the card is finished, terminal, cancelled or dormant. What is missing is
the remote part: `benchd` listens only on a local Unix socket, so the agent's harness must run on
the bench host, as a one-shot task the hub starts.

## Goals

1. An agent on any machine can take a card's bench lease and work in it remotely: run commands,
   use a terminal, read and write files, and use git.
2. People can **look** into a card's bench, read-only, from their own terminal (browse files,
   `git log`, `cat`), without a lease and without disturbing the agent.
3. Works for our fleet (over the tailnet) and for outsiders. Outsider access is built in but **off
   by default**; an operator turns it on.
4. Works standalone: a bench host needs no AgentPod, SuperPipeline or Accounts. Outsiders run their
   own `benchd` on their own hosts.
5. Plain tools work: OpenSSH `ssh`, `scp` and `sftp` (git runs inside the bench). Hermes's
   existing SSH terminal backend (which every guild agent's harness has) works with configuration
   only.

## Non-goals (this design)

- Typing by people (a person taking the lease to work by hand). Later; see "Later".
- Watching an agent's live terminal session. Later, with typing.
- Hosted, multi-tenant SuperBench run by us for outsiders. The design must not block it (see
  "Hosted later"), but its tenant isolation is not built here.
- Reusable benches (per-repo templates, warm pools). Separate project designed next; this design
  only marks where they plug in.
- Moving a bench between hosts. Unchanged: still the reviewed administrative procedure.

## Anti-goals (failure even if it "works")

- Any path where a connection reaches a bench other than the one its credential names, or keeps
  working after its lease ended.
- `benchd` depending on the suite. The dependency points suite → SuperBench only.
- Outsider access reachable on a host whose operator never enabled it.
- A private key or lease token printed, logged, or stored in a bench.
- Weakening the existing single-writer lease: two writers in one bench at once.

## Constraints

- Charter conditions (charter decision 2026-11-15 and its 2026-11-22 amendment): the dependency
  points suite → SuperBench only; SuperBench enforces every guarantee itself; coordinator-shaped
  surfaces stay optional; CI proves it works standalone; with the flags off the suite costs nothing.
- `benchd` runs unprivileged; privilege goes only through the fixed-policy helpers (`bench-quota`,
  `bench-payload`). Payload commands run as the payload user, in the bench's private layout and
  its cgroup, with a rebuilt environment (see the payload identity concept page).
- Leases are cooperative and never expire on their own. This design does not change that: access
  credentials expire; leases do not.
- Linux amd64 bench hosts; Go; the existing `activation.json` switch model and installer rules.

## Approaches considered

| | Approach | Trade-off |
|---|---|---|
| **A (chosen)** | An SSH server inside `benchd` (Go `x/crypto/ssh`), `benchd` as its own certificate authority, issuing short-lived OpenSSH certificates bound to a lease | `benchd` checks the lease on every connection and channel and can cut live sessions at once; enforces read-only itself; needs nothing from the host's own `sshd`, so an outsider just runs `benchd`. Cost: we own an SSH server's security (hardening, fuzzing, review) and implement exec/PTY/SFTP/git on top of a mature library. |
| B | The host's OpenSSH `sshd`, configured to trust `benchd`'s CA, mapping certificates to per-bench users with `ForceCommand`/chroot | Most battle-tested server, less code. But invasive host configuration for outsiders, a lease ending does not close an open session by itself (needs revocation lists plus a killer), and per-watcher read-only means juggling mount namespaces outside `benchd`. |
| C | An HTTP/gRPC API with an MCP layer (E2B/Daytona style), no SSH | Structured and easy to meter, but every harness needs an adapter, Hermes's SSH backend goes unused, and there is no plain terminal or `git` over SSH. Possible later as a layer over A. |

The operator chose **A** on 2026-11-24.

## Design

### 1. The SSH door

- `benchd` gains an SSH listener, off unless `activation.json` enables `remote_access`. Two
  listeners, separately enabled:
  - **fleet**: binds a configured private address (for example the host's tailnet address).
  - **public** (outsiders): binds a configured address; off by default and refused unless the
    operator also sets `remote_access_public: true`. Same protocol and rules as fleet; it exists so
    an operator can expose one without the other.
- Host key: generated once, kept in `benchd`'s state, mode 0600; its fingerprint is printed by
  `bench host-status` so clients can pin it.
- Only certificate authentication. No passwords, no plain public keys, no agent forwarding, no X11,
  no port forwarding (local, remote or dynamic), no `subsystem` other than `sftp`.

### 2. Credentials: per-lease certificates

- `benchd` holds its own CA key (state dir, 0600; never leaves the host).
- **Writer certificate.** Acquiring a lease over the socket can request a remote credential: the
  client sends an SSH **public** key, `benchd` returns an OpenSSH user certificate signed by its CA.
  The certificate carries:
  - key ID: the lease's opaque ID (never the lease token);
  - a single principal naming the bench (`bench:<bench id>`) and the mode `write`;
  - validity: short (default 30 minutes), renewable while the lease stays held;
  - critical options: none that a stock client must understand; `benchd` reads its own extension
    (`superbench-lease@…`) carrying the lease ID and lease generation.
- **Watcher certificate.** A separate request returns a certificate with mode `read`, for the same
  bench, with no lease needed. It is issued only to callers `benchd` already authorises to read
  that bench (the existing reader rules: `--reader-uids`, or a suite coordinator's scoped reader
  grant), and is short-lived (default 15 minutes).
- **No private key ever crosses `benchd`.** Clients generate their key pair; `benchd` signs.
- **Every connection is re-checked.** On each new connection and each new channel `benchd` verifies:
  the certificate is signed by its CA and within validity; the named bench exists; for `write`,
  the lease is still held, its ID and generation match the certificate, and it is not stopped.
  Releasing, stopping or renewing a lease to a new generation **closes every open writer session**
  for that bench at once.
- Renewal: a holder renews its certificate through the same socket call (or, remotely, over an
  authenticated `bench` request on the open connection) before expiry. An expired certificate gets
  no new connection; open sessions close when the certificate's validity ends.
- Lease tokens never travel over SSH and never appear in certificates or logs.

### 3. What a session can do

- **Writer sessions** run as the payload user, inside the bench's private layout and its cgroup,
  through the existing `bench-payload` helper path, exactly like a supervised run (rebuilt
  environment, git global/system config off). Supported:
  - `exec` (one command) and `shell` with a PTY;
  - `sftp` confined to the bench's checkout;
  - `git` over SSH against the bench's own repositories is not needed: git runs inside the bench.
    (Pushing to remotes stays `bench checkpoint`.)
- **Watcher sessions** run as the payload user against a **read-only** view of the bench's checkout
  (a read-only bind mount in the session's private mount namespace, set up by `bench-payload` in a
  new fixed read-only mode). Writes fail at the kernel. Commands are allowed (so `git log`, `ls`,
  `cat` work) but cannot change the bench; no network egress beyond what supervised runs already
  have (see "Network").
- Writer and watcher sessions never see another bench: the private layout is per bench.
- Session count and wall-clock limits per bench, configurable, so a runaway client cannot exhaust
  the host.

### 4. Who asks for credentials

- **Standalone / outsiders:** a person or their own tooling on the bench host's socket (as today),
  or remotely through a small authenticated `bench remote-credential` request. `benchd` is the
  only authority; no suite product is involved.
- **Our suite:** the AgentPod hub already takes leases for bench stages. With remote access it asks
  `benchd` (through the node, as today) for a writer certificate for the agent's public key and
  hands it to the claiming agent with the bench's address and host-key fingerprint. Every check is
  still `benchd`'s.

### 5. How an agent uses it (suite flow)

1. A card reaches a stage that works in the card's bench.
2. The claiming agent (on any node) creates a key pair, or uses its node's.
3. The hub takes or renews the card's bench lease and returns: bench address, host-key
   fingerprint, and a writer certificate for that agent's public key.
4. The agent's harness points its terminal at the bench. For Hermes: its SSH terminal backend with
   the certificate, user `bench:<bench id>`, and the bench's checkout as its working directory.
5. The agent works, then `checkpoint` and release go through the hub as today. The next stage's
   agent, anywhere, gets the same bench.

This adds an "agent works remotely in the card's bench" mode next to today's "hub starts a
one-shot task on the bench host" mode. Both stay; a stage chooses.

### 6. Network

- No new outbound access for benches. Remote access is inbound only; a bench's egress stays what
  supervised runs already allow.
- Fleet listener: tailnet address only, by configuration. Public listener: the operator's choice of
  address, intended behind their own firewall; `benchd` rate-limits failed authentications per
  source.

### 7. Audit

Every certificate issued and every session (who, bench, mode, start, end, bytes) is recorded in
`benchd`'s state and visible through `bench status`. Commands typed are not recorded (privacy and
size); exec commands are recorded by name only.

### 8. Off costs nothing

With `remote_access` off: no listener, no CA key generated, no new code path runs. CI proves
standalone use (a test client with stock OpenSSH against `benchd` without any suite product) and
proves the off state opens no port.

## Decisions from the builder check (2026-11-24)

A reviewer read this design against the code and docs. These are the designer's calls on what it
found; the operator can overturn any of them.

1. **Remote access requires the Fresh payload path.** `remote_access` refuses to start unless
   `fresh_payload` is on (and its own prerequisites hold). Remote sessions never run as the daemon
   account. Our bench hosts already run with `fresh_payload: true`; the docs that say the payload
   user is not active end to end are updated as part of this work.
2. **Per-bench separation comes from the session's mount namespace, not the uid.** All benches
   share one payload uid today. Each remote session gets a private mount namespace in which only
   its bench (read-write for writers, read-only bind for watchers) and the payload's private home
   and tmp exist; no other bench path is mounted. Per-bench uids are deferred to hosted.
3. **A new confined session launcher, not bench-payload's finite mode.** `bench-payload` gains a
   second fixed mode, `session`, still argument-free and policy-fixed. It receives pre-opened
   descriptors (a PTY or socket pair) from `benchd` over `SCM_RIGHTS`, sets up the session's mount
   namespace and cgroup, drops to the payload user and executes the fixed shell or the SFTP server.
   It gets its own security review.
4. **Remote writer sessions are the lease's execution.** Granting a writer certificate records a
   `remote` execution on the lease. `checkpoint` and `release` first close and fence every open
   writer session (their cgroup emptied), then proceed as today. `stop` and `stop_admin` fence them
   the same way. A lease stopped after a one-shot `run` cannot be reused for remote work: the hub
   releases it and takes a new one for the next stage.
5. **Lease identity and generation.** Leases gain an opaque `ID` (per acquisition) and a
   `Generation` (incremented on every token rotation, including `renew_admin`). Certificates carry
   both; any mismatch refuses the connection. This is a state migration.
6. **Certificate renewal keeps connections alive.** `benchd` tracks each connection's certificate.
   A holder renews before expiry through the socket (or a `renew` request on the open connection)
   for the same key; `benchd` extends that connection's deadline. A connection whose deadline passes
   is closed. Hermes's SSH backend therefore does not drop every 30 minutes.
7. **User name `b-<bench id>`**, not `bench:<id>`: a colon breaks `scp`, `rsync` and git address
   syntax. The principal names the `bench_id`, never the key, because a reclaimed key gets a new
   bench.
8. **The unit is the card's v2 workspace.** Sessions start in the workspace root, which holds its
   members (1 to 32 repositories). Watcher read-only binds cover that root.
9. **The hub keeps the lease token; the agent only gets a certificate.** When the agent reports the
   stage done, the hub checkpoints and releases (as today). A crashed remote agent is noticed by
   SuperPipeline's run heartbeat (15 minutes): the run is reclaimed, the hub stops the lease, and
   `benchd` cuts its sessions.
10. **Hermes shells out to OpenSSH** with `-i <key>`; OpenSSH loads the matching `-cert.pub`
    automatically, so a certificate, user name, pinned host key (`UserKnownHostsFile`) and working
    directory need configuration only. Proved by a spike before relying on it.
11. **Corrections:** goal 5 drops "git over SSH" (git runs inside the bench); egress is stated
    honestly as "unchanged from supervised runs, which have no network isolation today"; the
    security-model page's "No TCP listener" changes to "no TCP listener unless remote access is
    enabled".

## Operator decisions (2026-11-24)

**Who may ask for a certificate (remote callers with no socket access): both an operator key list
and OIDC.**

- **Key list (standalone, always available).** The operator lists client SSH public keys in a
  root-owned `benchd` policy file. Each entry names a principal and what it may do: `read` and/or
  `write`, on which benches (see grants below). A listed key proves itself on a credential request
  (SSH public-key auth on a dedicated `credential` user, or a signed request over the socket's
  remote counterpart) and receives a short-lived certificate for one bench. This works with no
  other product and is the fallback on every host.
- **OIDC (optional).** `benchd` can also trust one or more OIDC issuers (our Accounts for the
  fleet; an outsider's own provider). A caller presents an ID token; `benchd` verifies it against
  the issuer's published keys, maps the configured claim (subject, and group or role claims) to a
  principal, and applies the same grants. Off unless an issuer is configured. No issuer is trusted
  by default, including ours.
- Both feed one grant check; neither bypasses leases. A `write` grant still needs the bench's lease
  to be held for that holder (in the suite, the hub takes it and asks for the certificate on the
  agent's behalf).

**Who may look at which benches: granted per bench, board or card, not host-wide.**

- A grant names a scope: one bench (`bench_id`), or a selector the operator sets on workspaces at
  creation (for example `board=<id>` or `card=<id>`, stored as workspace labels). A watcher
  certificate is issued only for a bench inside the principal's scope.
- The hub, as a coordinator, may add or remove watcher grants for its own cards' benches through
  the socket, within an operator-set ceiling (it can never grant beyond the boards it is allowed).
- Host-wide reader uids (`--reader-uids`) keep their current meaning (local `status` only) and
  grant nothing remote.

## Hosted later

Hosted SuperBench (our hosts, outsiders' cards) would add: per-tenant bench users or VMs, network
policy per tenant, quotas per tenant, and tenant-scoped CA or principals. The protocol, certificate
model and per-bench confinement here do not change.

## Reusable benches (next project; where they plug in)

Per-repo templates (a bench from `main` with setup done, rebuilt as `main` moves) copied by XFS
reflink into a new card's bench, plus an optional warm pool. They change only how a bench is
created; remote access is unchanged.

## Later

- People typing: a person takes the lease (writer certificate) through the same flow.
- Watching an agent's live terminal.
- An MCP or HTTP layer over the same sessions for harnesses with no SSH backend.

## Left to the builder

- Exact `activation.json` and `benchd` flag names; certificate extension name; defaults for
  validity, session limits and rate limits (proposed above).
- How `bench-payload` grows the read-only mode while keeping its fixed-policy, argument-free shape.
- SFTP implementation choice (for example `pkg/sftp` server confined to the checkout).
- Test plan: stock OpenSSH client tests, lease-end cuts sessions, wrong-bench and expired
  certificates refused, read-only enforced by the kernel, off state opens nothing, fuzzing of the
  SSH handshake and channel handling.
