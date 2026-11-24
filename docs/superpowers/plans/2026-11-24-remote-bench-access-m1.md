# Remote Bench Access, Milestone 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A fleet writer holding a card's v2 workspace lease works in that workspace remotely over SSH (exec, shell with a PTY, SFTP) through a private fleet listener in `benchd`. A stock OpenSSH client works end to end, and releasing or stopping the lease cuts the sessions.

**Architecture:** `benchd` gains its own CA and host key. It issues 30-minute OpenSSH user certificates, bound to a lease's ID, generation and remote execution, through a new default-off route on its existing Unix socket. A `golang.org/x/crypto/ssh` listener accepts only those certificates. It re-checks the lease on every connection, every channel and a 1-second sweep, and starts each session through `bench-payload`'s new argument-free `session` mode. That mode receives the session's descriptors over `SCM_RIGHTS`, builds a private mount namespace that shows only that bench, drops to the payload user and execs a fixed shell or the confined SFTP server (`bench-sftp`). Writer sessions are the lease's `remote` execution: one cgroup scope recorded on `Lease.Execution`, fenced (`cgroup.kill`) and cleared before checkpoint, release, stop and stop_admin.

**Tech Stack:** Go 1.26.4 (stdlib, `os.Root`), `golang.org/x/crypto/ssh`, `github.com/pkg/sftp` (server side, in `bench-sftp` only), SQLite state (existing `modernc.org/sqlite` adapter), cgroup v2, Linux mount API (`open_tree`/`move_mount`), stock OpenSSH client for tests, Astro docs site.

**Spec:** `docs/superpowers/specs/2026-11-24-remote-bench-access-design.md`. The "Decisions from the builder check" and "Operator decisions" sections override the earlier sections where they differ.

## Global Constraints

- Toolchain Go 1.26.4. `bench-payload` stays `CGO_ENABLED=0`. Every command builds for linux/amd64 and linux/arm64 (`scripts/check.sh`).
- Architecture test (`internal/architecture/architecture_test.go`): no file over 1,000 lines, no package over 4,000 lines **including tests**, and only the import edges it lists. Packages near the cap: `internal/bench` 4000, `internal/hoststorage` 3997, `internal/benchstate` 3993, `cmd/bench` 3993, `internal/service` 3990, `internal/payloadexec` 3841. New code goes in new packages. Edits to these packages are limited to the line budgets each task states.
- External imports are pinned exactly in go.mod/go.sum, never floating. Each is allowed only where `docs/code-quality.md` and the architecture test say: `golang.org/x/crypto/ssh` in `internal/remoteca` and `internal/remotessh` only; `github.com/pkg/sftp` in `internal/remotesftp` only. Pin the newest tagged release at implementation time and record the exact version in code-quality.md.
- With `remote_access` off: nothing listens, no CA or host key is generated, and no new code path runs (spec §8).
- `remote_access` refuses to start unless `fresh_payload` is on (decision 1).
- Fleet listener only in M1. `remote_access_public` and any public-listener key stay unknown keys, which `activation.Parse` refuses.
- Writer certificate: validity 30 minutes (spec §2). Single principal and user name `b-<bench id>`, where bench id is `Workspace.ID` (decision 7). Key ID is the lease's opaque `ID`. No critical options. Extension `superbench-lease@superbench.dev` carries the lease ID and generation (decision 5) and, in this plan, the remote execution ID too.
- A lease token never travels over SSH and never appears in a certificate, log line, audit row or remote response (anti-goals, spec §2).
- Certificate-only authentication. No passwords, no plain public keys, no agent forwarding, no X11, no local, remote or dynamic port forwarding, and no subsystem other than `sftp` (spec §1).
- Re-check the lease on every connection and every channel. Writer sessions are the lease's `remote` execution. checkpoint, release, stop and stop_admin first close and fence them (decision 4).
- Leases still never expire on their own. Only certificates expire.
- `benchd` stays unprivileged. Privilege goes only through the fixed-policy, argument-free `bench-payload` (Constraints; decision 3).
- Sessions start in the workspace root, which holds 1 to 32 members (decision 8).
- Docs follow `docs-site/scripts/honesty.mjs`: no local host, agent or team names (the harness is named generically, never by its product name), "isolation" and "sandbox" only negated, and no version numbers except released tags.
- One commit per task, message style `area: summary`. Run `sh scripts/check.sh` before publishing. Tests are never skipped in CI: a missing `ssh` or `sftp` binary fails when `CI=true`.

## Review Focus

1. **Lease rotated while a connection is open** (renew_admin, or a new acquisition after release): the old certificate's connection closes within the 1-second sweep, even with no channel open. Test: Task 11 `TestSweepClosesConnectionAfterRenewAdmin`.
2. **Client vanishes mid-session** (TCP reset, laptop sleep): the audit row ends with `closed`, and a detached process the session left behind (`nohup sleep`) is killed by the next fence, not leaked past release. Test: Task 11 `TestAbruptDisconnectThenReleaseFencesLeftovers`.
3. **benchd restarts while a remote execution is recorded**: its scope is gone, so the old certificate is refused, and the next grant creates a new execution instead of reusing a dead one. Test: Task 5 `TestGrantReplacesRemoteExecutionWhoseScopeIsGone`.
4. **A symlink inside the workspace that points outside it**, followed through SFTP, is refused. The same path in a shell resolves inside the session's namespace, where the data and state roots show only this bench. Test: Task 8 `TestSFTPRefusesSymlinkEscape`, Task 7 fixture marker `SESSION_NAMESPACE_SHOWS_ONLY_ITS_BENCH`.
5. **A write fails part way through an SFTP upload** (quota, read-only path): the client gets an SFTP failure status and the server keeps serving the next request. Test: Task 8 `TestSFTPWriteFailureKeepsServing`.

---

## File structure

New packages (and their architecture-test edges):

| Path | Responsibility | Imports (edges) |
| --- | --- | --- |
| `internal/peercred` | Kernel peer uid for the Unix socket (moved out of `service`) | none (`x/sys/unix` in `peer_darwin.go`, as before) |
| `internal/remoteschema` | Schema 31 upgrade (lease identity, audit tables), audit rows, `CommandName` | `internal/model` |
| `internal/remoteca` | CA and host key files, writer certificate issue and verify, `Principal` | none (+ `golang.org/x/crypto/ssh`) |
| `internal/remotestate` | Remote execution on the lease: grant, admit, fence, audit writes, status | `internal/bench`, `internal/benchstate`, `internal/execution`, `internal/model`, `internal/operationcontrol`, `internal/remoteca`, `internal/remoteschema`, `internal/workspaceprep` |
| `internal/remoteproto` | Wire types and strict parsing for `/v2/remote/*` | none |
| `internal/remoteapi` | Socket handler: writer certificate, remote status | `internal/remoteca`, `internal/remoteproto`, `internal/remotestate` |
| `internal/remotecli` | `bench workspace remote-credential` and `remote-status` | `internal/client`, `internal/remoteproto` |
| `internal/payloadsession` | Helper side: descriptor receipt, session stdio checks, mount namespace | `internal/payloadpolicy` |
| `internal/remotessh` | SSH server, channels, PTY, connection registry, `Launcher` interface | `internal/remoteca`, `internal/remoteschema`, `internal/remotestate` (+ `x/crypto/ssh`) |
| `internal/remotessh/remotesshtest` | Test-only local launcher (runs `/bin/sh` as the caller, SFTP in process) | `internal/remotessh`, `internal/remotesftp` |
| `internal/remotesession` | benchd side: sudo helper launch in the remote scope, frame plus `SCM_RIGHTS` | `internal/execution`, `internal/payloadexec`, `internal/payloadwire`, `internal/serviceidentity`, `internal/remotessh`, `internal/remotestate` |
| `internal/remotesftp` | SFTP request handlers rooted at the cwd through `os.Root` | none (+ `github.com/pkg/sftp`) |
| `internal/remoteaccess` | Composition: listener lifecycle, implements `service.Remote` | `internal/activation`, `internal/bench`, `internal/remoteapi`, `internal/remoteca`, `internal/remotesession`, `internal/remotessh`, `internal/remotestate` |
| `cmd/bench-sftp` | Installed SFTP server executable, argument-free, refuses root | `internal/remotesftp` |

Edges added to existing packages: `internal/service` + `internal/peercred`; `internal/benchstate` + `internal/remoteschema`; `internal/workspaceops` + `internal/remotestate`; `internal/payloadexec` + `internal/payloadsession`; `internal/client` + `internal/remoteproto`; `cmd/bench` + `internal/remotecli`; `cmd/benchd` + `internal/remoteaccess`.

Modified files: `internal/model/bench.go`, `internal/model/lease.go`, `internal/execution/execution.go`, `internal/workspacerenew/renew.go`, `internal/benchstate/migration.go`, `internal/benchstate/records.go`, `internal/activation/activation.go`, `internal/service/{server.go,server_v2.go,socket.go}`, `internal/workspaceops/manager.go`, `internal/payloadwire/request.go` (plus a new `session.go`), `internal/payloadexec/session_linux.go` (new), `cmd/bench-payload/main.go`, `cmd/bench/workspace.go`, `cmd/benchd/main.go`, `internal/architecture/{architecture_test.go,confine_test.go}`, `deploy/linux/{install.py,install_test.py}`, `scripts/check-identity-ci.py`, `scripts/release.sh` / `.github/workflows/release.yml` (bench-sftp binary), docs.

## Which tests need what

| Lane | Runs | Tests |
| --- | --- | --- |
| Ordinary CI (`check / check`, unprivileged Linux, `nobody`) | every PR | Tasks 1–6, 8, 10, 11, including fuzz seed corpora. Tests that use `/proc/net`, PTYs, `SCM_RIGHTS` and cgroup-free fakes are `_linux_test.go`. Stock `ssh`, `scp` and `sftp` binaries are required. |
| Privileged (`check / privileged-identity`, root, self-hosted runner on the staging host, because forge has no root runner) | protected `main` pushes only | Task 7 fixture `TestInstalledRemoteSessionFixture` (tag `serviceidentityfixture`), added to `scripts/check-identity-ci.py` `TESTS`. Before merging Tasks 7 and 9, run `python3 scripts/check-ci.py --privileged-only` on the staging host by hand. |
| Disposable-host drill (operator, staging host) | Task 13 | `scripts/test-remote-access.py`: the full stack with installed helper, sudo, cgroup and stock ssh. Output is captured to `docs/remote-access-output.txt`. |

---

### Task 1: Make room in `internal/service`

Pure move, no behaviour change. It frees 26 lines for the remote hook in Task 6/11.

**Files:**
- Create: `internal/peercred/peer_linux.go`, `internal/peercred/peer_darwin.go` (the bodies of `internal/service/peer_{linux,darwin}.go`)
- Delete: `internal/service/peer_linux.go`, `internal/service/peer_darwin.go`
- Modify: `internal/service/socket.go:72` (`peerUID` becomes `peercred.UID`)
- Modify: `internal/architecture/architecture_test.go` (edge `internal/service` + `internal/peercred`; add `"internal/peercred": {}`; move the `x/sys/unix` exception from `rel == "internal/service" && peer_darwin.go` to `internal/peercred`)

**Interfaces:**
- Produces: `func peercred.UID(fd int) (uint32, error)`

- [ ] **Step 1:** Move the two files, rename the package to `peercred` and export `UID`.
- [ ] **Step 2:** Run `go test ./internal/service ./internal/architecture` and `GOOS=darwin go vet ./internal/peercred ./internal/service`. Expected: PASS. The architecture test reports no forbidden dependency.
- [ ] **Step 3:** Run `wc -l internal/service/*.go | tail -1`. Expected: `3964 total` (±2).
- [ ] **Step 4: Commit** `git commit -m "service: move peer credentials to their own package"`

---

### Task 2: Lease identity and generation, state schema 31

Decision 5. Leases gain an opaque `ID` (per acquisition) and a `Generation` (1 on acquisition, +1 on every token rotation, `renew_admin` included). Schema 31 also creates the audit tables used from Task 5.

**Files:**
- Modify: `internal/model/bench.go:10-17` (Lease fields), `internal/model/lease.go` (NewLease, Rotate)
- Modify: `internal/workspacerenew/renew.go:93-101` (use `Rotate`)
- Create: `internal/remoteschema/schema.go`, `internal/remoteschema/schema_test.go`, `internal/remoteschema/migrate_test.go` (package `remoteschema_test`; it may import `benchstate` because test imports are not edge-checked and an external test package has no cycle)
- Modify: `internal/benchstate/records.go:20` (`SchemaVersion = 31`), `internal/benchstate/migration.go` (budget: **+4 lines**, since the package is at 3993)
- Test: `internal/model/lease_test.go` (new), `internal/workspacerenew/renew_test.go`

**Interfaces:**
- Produces:
  - `model.Lease` gains `ID string \`json:",omitempty"\`` and `Generation uint64 \`json:",omitempty"\``, declared before `Token`.
  - `func model.NewLease(holder string) (*Lease, error)`: `ID = Nonce()`, `Generation = 1`.
  - `func (l *Lease) Rotate() (*Lease, error)`: same `Holder` and `ID`, `Generation = l.Generation+1`, new `Token`. `Continuation`, `Execution`, `RetainedRuns` and `StopRequested` are zero; the caller copies what it keeps, as `Renew` does today.
  - `const remoteschema.SchemaVersion = 31`
  - `func remoteschema.Upgrade31(tx *sql.Tx) error`: gives every lease record without an ID `ID = model.Nonce()` and `Generation = 1` (per row, `json_set` on `benches.record`), creates `remote_certificates` and `remote_sessions` (columns are the fields of the Task 5 records), then `PRAGMA user_version=31`.

- [ ] **Step 1: Write the failing tests**

```go
func TestNewLeaseHasIdentity(t *testing.T) // ID matches ^[a-f0-9]{32}$, Generation == 1, ID != Token
func TestRotateKeepsIDAndAdvancesGeneration(t *testing.T) // r.ID == l.ID, r.Holder == l.Holder, r.Generation == l.Generation+1, r.Token != l.Token, r.Continuation == "", r.Execution == nil
// workspacerenew/renew_test.go
func TestRenewAdvancesGenerationAndKeepsID(t *testing.T) // after Renew: Lease.ID == prior.ID, Generation == prior.Generation+1
// remoteschema/migrate_test.go (package remoteschema_test)
func TestUpgradeFromThirtyAssignsLeaseIdentity(t *testing.T)
//   a state DB at user_version 30 with one leased record (no ID) and one unleased record ->
//   benchstate.Migrate: user_version == 31; leased record ID ^[a-f0-9]{32}$, Generation == 1;
//   unleased record bytes unchanged; tables remote_certificates, remote_sessions exist
func TestUpgradeFromLegacyReachesThirtyOne(t *testing.T) // version 0 path ends at 31 with tables present
func TestUpgradeIsIdempotentAtThirtyOne(t *testing.T)    // Migrate twice: second is a no-op, IDs unchanged
```

- [ ] **Step 2:** Run `go test ./internal/model ./internal/workspacerenew ./internal/remoteschema -run 'Lease|Rotate|Renew|Upgrade' -v`. Expected: FAIL (`Rotate`/`Upgrade31` undefined).
- [ ] **Step 3: Implement.** Add the Lease fields, `NewLease` and `Rotate`. In `Renew`, replace `model.NewLease(b.Lease.Holder)` with `b.Lease.Rotate()`. In `benchstate.Migrate`, replace both `tx.Exec("PRAGMA user_version=30")` calls with `remoteschema.Upgrade31(tx)`, and add `if version == 30 { return remoteschema.Upgrade31(tx) }` immediately after the `version == SchemaVersion` early return. Add the architecture edges `internal/benchstate` + `internal/remoteschema` and `"internal/remoteschema": {"internal/model"}`.
- [ ] **Step 4:** Run `go test -race ./internal/model ./internal/workspacerenew ./internal/remoteschema ./internal/benchstate ./internal/workspace ./internal/workspaceops ./internal/architecture`. Expected: PASS. Then run `wc -l internal/benchstate/*.go | tail -1`. Expected: ≤ 4000.
- [ ] **Step 5: Commit** `git commit -m "state: leases carry an ID and a generation (schema 31)"`

---

### Task 3: Activation switches `remote_access` and `remote_fleet_address`

**Files:**
- Modify: `internal/activation/activation.go`
- Test: `internal/activation/remote_test.go` (new)

**Interfaces:**
- Produces: the `Switches` fields `RemoteAccess bool` (`remote_access`) and `RemoteFleetAddress netip.AddrPort` (`remote_fleet_address`, a JSON string). `Any()` includes `RemoteAccess`. `String()` appends ` remote_access=%t remote_fleet_address=%s`. The address is valid when `netip.ParseAddrPort` accepts it, the port is not 0, the address is not unspecified (`0.0.0.0`, `::`), and there is no zone.
- `Check` refusals, in this order, before the existing `fresh_payload` cases:
  - `"remote_access requires fresh_payload"`
  - `"remote_access requires remote_fleet_address"`
  - `"remote_fleet_address requires remote_access"`

- [ ] **Step 1: Write the failing tests**

```go
func TestRemoteAccessParses(t *testing.T) // {"version":..,"fresh_payload":true,"cancellation":true,"remote_access":true,"remote_fleet_address":"100.64.0.7:2222"} -> RemoteAccess, AddrPort == 100.64.0.7:2222, Any()
func TestRemoteFleetAddressRefusals(t *testing.T) // "0.0.0.0:2222", "[::]:2222", "host:2222", "100.64.0.7:0", "100.64.0.7", 2222 (number), null, "[fe80::1%eth0]:22" -> ErrActivation
func TestRemoteAccessRequiresFreshPayload(t *testing.T) // Check -> error containing "remote_access requires fresh_payload"
func TestRemoteAccessRequiresAddress(t *testing.T)
func TestAddressRequiresRemoteAccess(t *testing.T)
func TestPublicListenerKeyIsUnknown(t *testing.T) // "remote_access_public":true -> "unknown key remote_access_public"
func TestDefaultsKeepRemoteOff(t *testing.T) // missing file and every-off file: RemoteAccess false, address invalid (zero)
```

- [ ] **Step 2:** Run `go test ./internal/activation -run Remote -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** In `Parse`, handle `remote_fleet_address` like `fresh_reclaim_idle_hours` (a dedicated branch that requires a JSON string), then add the `Check` cases.
- [ ] **Step 4:** Run `go test -race ./internal/activation ./cmd/benchd`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "activation: remote_access and remote_fleet_address, off by default"`

---

### Task 4: `benchd` CA, host key and writer certificates (security gate A)

**Files:**
- Create: `internal/remoteca/keys.go`, `internal/remoteca/certificate.go`, `internal/remoteca/keys_test.go`, `internal/remoteca/certificate_test.go`, `internal/remoteca/fuzz_test.go`
- Modify: `go.mod`, `go.sum` (`golang.org/x/crypto`, exact version), `internal/architecture/architecture_test.go` (`"internal/remoteca": {}` and an exception allowing `golang.org/x/crypto/ssh` for `rel == "internal/remoteca"`)

**Interfaces:**
- Produces:
  ```go
  const ExtensionName = "superbench-lease@superbench.dev"
  const WriterValidity = 30 * time.Minute
  const ClockSkew = time.Minute // ValidAfter = now - ClockSkew
  type Claims struct { BenchID, LeaseID string; Generation uint64; ExecutionID string }
  func Principal(benchID string) string            // "b-" + benchID
  type Authority struct{ /* ca, host ssh.Signer */ }
  func Open(dir string) (*Authority, error)         // dir = <state>/remote
  func (a *Authority) HostSigner() ssh.Signer
  func (a *Authority) HostKeyLine() string          // "ssh-ed25519 AAAA…"
  func (a *Authority) HostFingerprint() string      // ssh.FingerprintSHA256, "SHA256:…"
  func Fingerprint(authorizedKey string) (string, error)
  type Issued struct { Line string; Serial uint64; Fingerprint string; ValidAfter, ValidBefore time.Time }
  func (a *Authority) IssueWriter(authorizedKey string, c Claims, now time.Time) (Issued, error)
  func (a *Authority) Verify(user string, key ssh.PublicKey, now time.Time) (Claims, *ssh.Certificate, error)
  var ErrKey, ErrCertificate error
  ```
- Key files: directory `<state>/remote`, mode 0700. `ca_ed25519` and `host_ed25519` are OpenSSH-format private keys, mode 0600, created `O_CREATE|O_EXCL|O_NOFOLLOW`, with file and directory fsynced. On load, each must be a regular file owned by the euid, mode 0600, nlink 1. Anything else refuses (`ErrKey`). `Open` is the only creator. It is called only when `remote_access` is on (Task 11).
- Accepted client keys: `ssh-ed25519` and `ecdsa-sha2-nistp256` only, input ≤ 8 KiB. A certificate given as the client key is refused.
- Issued certificate: `CertType = UserCert`, random `Serial`, `KeyId = c.LeaseID`, `ValidPrincipals = [Principal(c.BenchID)]`, `ValidAfter = now-ClockSkew`, `ValidBefore = now+WriterValidity`, `CriticalOptions = {}`, `Extensions = {"permit-pty": "", ExtensionName: "v1 write <lease id> <generation> <execution id>"}`.

- [ ] **Step 1: Write the failing tests**

```go
func TestOpenCreatesKeysOnceWithPrivateModes(t *testing.T) // dir 0700, files 0600; second Open returns same HostFingerprint
func TestOpenRefusesUnsafeKeyFiles(t *testing.T) // symlink, 0644, hardlink (nlink 2) -> ErrKey
func TestIssueWriterShape(t *testing.T)
//   cert.KeyId == c.LeaseID; ValidPrincipals == []string{"b-" + c.BenchID}
//   ValidBefore-ValidAfter == uint64((30*time.Minute + time.Minute).Seconds())
//   len(CriticalOptions) == 0; Extensions has exactly "permit-pty" and ExtensionName
//   no "permit-port-forwarding", "permit-agent-forwarding", "permit-X11-forwarding", "permit-user-rc"
//   !strings.Contains(issued.Line, anyLeaseToken)
func TestIssueWriterRefusesKeys(t *testing.T) // ssh-rsa, ssh-dss, a certificate, garbage, 9 KiB -> ErrKey
func TestVerifyAcceptsOwnCertificate(t *testing.T) // returns Claims equal to issued
func TestVerifyRefuses(t *testing.T) // table, each -> ErrCertificate:
//   other CA; user "b-<other id>" (wrong bench); now > ValidBefore (expired); now < ValidAfter;
//   host cert type; missing extension; extension "v1 write <lease> 0 <exec>"; extra critical option;
//   extra extension "permit-port-forwarding"; KeyId != lease in extension; plain public key (not a cert)
func FuzzVerify(f *testing.F) // random bytes -> ssh.ParsePublicKey -> Verify: never returns nil error unless the bytes are a cert signed by a's CA
```

- [ ] **Step 2:** Run `go test ./internal/remoteca -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** `Verify` follows exactly this order:

```go
cert, ok := key.(*ssh.Certificate)              // else ErrCertificate (no plain keys, ever)
if cert.CertType != ssh.UserCert || !bytes.Equal(cert.SignatureKey.Marshal(), a.ca.PublicKey().Marshal()) { refuse }
if !userPattern.MatchString(user) /* ^b-[a-f0-9]{32}$ */ || len(cert.ValidPrincipals) != 1 || cert.ValidPrincipals[0] != user { refuse }
if len(cert.CriticalOptions) != 0 { refuse }
for name := range cert.Extensions { if name != "permit-pty" && name != ExtensionName { refuse } }
checker := ssh.CertChecker{Clock: func() time.Time { return now }}
if err := checker.CheckCert(user, cert); err != nil { refuse }  // window and signature
c, err := parseExtension(cert.Extensions[ExtensionName])         // exactly 5 space-separated fields, "v1","write", 32-hex, uint64 >= 1, 32-hex
if err != nil || cert.KeyId != c.LeaseID { refuse }
c.BenchID = user[2:]
```

- [ ] **Step 4:** Run `go test -race ./internal/remoteca ./internal/architecture` and `go test ./internal/remoteca -run '^$' -fuzz FuzzVerify -fuzztime 60s` (local only, not CI). Expected: PASS, no crashers.
- [ ] **Step 5: Review gate A.** A fresh reviewer, on the most capable model, reads `internal/remoteca` against spec §2 and decisions 5–7, and checks: a cert can only be accepted when signed by this CA for exactly one `b-` principal; no forwarding extension can ever be issued; a token cannot reach a cert; key files fail closed. Record the findings in the PR description. Do not start Task 10 until the findings are resolved.
- [ ] **Step 6: Commit** `git commit -m "remote: benchd CA and host key, 30-minute writer certificates"`

---

### Task 5: The remote execution on the lease, and fencing it first

Decision 4. A grant records `Lease.Execution{Kind: "remote"}` with its own cgroup scope. Admission re-checks it. checkpoint, release, stop and stop_admin fence it and **clear** it first (release requires sealed history for any `Lease.Execution`, which a remote execution has none of).

**Files:**
- Modify: `internal/execution/execution.go` (`Kind string \`json:",omitempty"\``, `const KindRemote = "remote"`)
- Create: `internal/remotestate/{grant.go,admit.go,fence.go,audit.go,status.go}` and tests `{grant_test.go,admit_test.go,fence_test.go,audit_test.go}`
- Create: `internal/remoteschema/audit.go`, `internal/remoteschema/audit_test.go`
- Modify: `internal/workspaceops/manager.go:209-241` (fence before checkpoint/release/stop/stop_admin; new `RemoteScopes` field)
- Test: `internal/workspaceops/remote_test.go` (new)
- Modify: architecture edges (`internal/remotestate`, `internal/workspaceops` + `internal/remotestate`)

**Interfaces:**
- Consumes: `remoteca.Claims` (Task 4), `model.Lease.ID/Generation` (Task 2), `bench.Store.WorkspaceRepair()` (existing), `execution.Host` (existing).
- Produces (`internal/remoteschema`):
  ```go
  type CertificateRecord struct { Serial uint64; Key, BenchID, LeaseID string; Generation uint64; ExecutionID, Holder, Fingerprint string; IssuedAt, ValidBefore time.Time }
  type SessionRecord struct { ID int64; Serial uint64; Key, BenchID, Fingerprint, Source, Kind, CommandName string; StartedAt, EndedAt time.Time; BytesIn, BytesOut uint64; EndReason string }
  const MaxAuditRows = 10000 // per table; inserting prunes the oldest ended rows
  func RecordCertificate(tx *sql.Tx, r CertificateRecord) error
  func StartSession(tx *sql.Tx, r SessionRecord) (int64, error)
  func EndSession(tx *sql.Tx, id int64, at time.Time, in, out uint64, reason string) error
  func EndOpenSessions(tx *sql.Tx, benchID string, at time.Time, reason string) error
  type Reader interface { Query(string, ...any) (*sql.Rows, error); QueryRow(string, ...any) *sql.Row }
  func Recent(r Reader, key string, limit int) ([]CertificateRecord, []SessionRecord, error)
  func CommandName(command string) string // basename of first field, <= 64 bytes, non-printable -> "?"
  ```
  The session `Kind` is one of `shell|exec|sftp`. `EndReason` is one of `exit|closed|fenced|expired|revoked|refused`.
- Produces (`internal/remotestate`):
  ```go
  var ErrConflict, ErrRevoked, ErrUnavailable error
  type Scopes interface { Create(root, id string) (execution.Identity, error); Fence(*execution.Execution) error; Empty(*execution.Execution) error }
  type Manager struct { Prep workspaceprep.Manager; Scopes Scopes; Now func() time.Time }
  func ForStore(store bench.Store, scopes Scopes) (Manager, error) // nil scopes -> execution.Host{}
  type Grant struct { Key, Holder string; Claims remoteca.Claims }
  func (m Manager) Grant(ctx context.Context, key, token string) (Grant, error)
  type Admission struct { Key, BenchID, LayoutID, Holder, Root string; Claims remoteca.Claims; Execution execution.Execution }
  func (m Manager) Admit(c remoteca.Claims) (Admission, error)
  func (m Manager) Fence(ctx context.Context, key, token string, admin bool) error
  func (m Manager) RecordCertificate(r remoteschema.CertificateRecord) error
  func (m Manager) SessionStarted(a Admission, r remoteschema.SessionRecord) (int64, error)
  func (m Manager) SessionEnded(id int64, in, out uint64, reason string) error
  type RemoteExecution struct { Key, BenchID, Holder, LeaseID, ExecutionID string; Generation uint64 }
  type Status struct { Remote []RemoteExecution; Certificates []remoteschema.CertificateRecord; Sessions []remoteschema.SessionRecord }
  func (m Manager) Status(key string, limit int) (Status, error)
  ```
- Grant rules (under `Prep.Gate(key)`, checked again inside `Prep.Update`):
  - Token owns the lease (constant-time); otherwise `bench.ErrLease`.
  - `Workspace.Phase == "ready"`, no `Pending`, `Archive`, `Transfer` or `InitializationRepair`, `Holder != "initialization"`, `!StopRequested`, `Continuation != ""`, and `operationcontrol.RequireContext(tx, b, nil)` succeeds; otherwise `ErrConflict`, with no `Scopes` call.
  - `Execution == nil`: `Scopes.Create(Prep.CgroupRoot, Nonce())`, then record `Execution{ID, Root: CgroupRoot, BootID, HostID, Phase: "running", Kind: "remote"}`. If the update fails, fence the created scope.
  - `Execution.Kind == "remote"` and running, with its scope present on this boot: reuse it (renewal, or a second key for the same holder).
  - A remote execution whose scope is gone or is from another boot: fence it (a no-op), then create a new one with a new ID, which invalidates older certificates.
  - Any other execution: `ErrConflict`.
- Admit refuses with `ErrRevoked` unless all of these hold: the bench with `Workspace.ID == c.BenchID` exists, `Lease.ID == c.LeaseID`, `Lease.Generation == c.Generation`, `!StopRequested`, and `Execution` is `{Kind remote, Phase running, ID == c.ExecutionID}`. On success, `Root = b.Path` and `LayoutID = Workspace.ID`.
- Fence: when there is no remote execution, return nil without touching anything. Otherwise call `Scopes.Fence(e)`, then in one transaction (same token unless `admin`, same execution ID) set `Lease.Execution = nil` and `EndOpenSessions(benchID, now, "fenced")`. If `Scopes.Fence` fails, return the error with state unchanged.
- workspaceops: `Manager.RemoteScopes remotestate.Scopes` (nil means the host). In `Execute`, after `CheckWorkspaceOwner` for checkpoint, release and stop, and before `StopWorkspace(key, "", true)` for stop_admin, call `remotestate.ForStore(m.Store, m.RemoteScopes)` and then `.Fence(ctx, key, token, admin)`. Map `remotestate.ErrConflict` to `ErrConflict`.

- [ ] **Step 1: Write the failing tests** (remotestate tests use a recording fake `Scopes` and a workspace fixture built like `workspaceops/manager_test.go:fixture`)

```go
func TestGrantRecordsRemoteExecution(t *testing.T) // Execution.Kind=="remote", Phase=="running", Root==CgroupRoot; Create called once with that ID; Claims{BenchID: Workspace.ID, LeaseID: Lease.ID, Generation: Lease.Generation, ExecutionID}
func TestGrantReusesLiveRemoteExecution(t *testing.T) // second Grant: same ExecutionID; Create count still 1
func TestGrantRefusalsHaveNoEffects(t *testing.T) // wrong token -> bench.ErrLease; StopRequested, initialization holder, run execution present, phase != ready -> ErrConflict; Create never called; record unchanged
func TestGrantReplacesRemoteExecutionWhoseScopeIsGone(t *testing.T) // Review Focus 3: fake reports scope absent -> new ExecutionID; Admit(old claims) -> ErrRevoked
func TestAdmitRefusesEveryMismatch(t *testing.T) // unknown bench id, lease ID, generation, execution ID, StopRequested, Execution nil, Kind "", Phase "stopped" -> ErrRevoked
func TestFenceKillsScopeThenClearsExecution(t *testing.T) // Fence called with the execution; Lease.Execution == nil; open session rows EndReason == "fenced"
func TestFenceWithoutRemoteIsNoop(t *testing.T) // a run execution is left untouched, Scopes not called
func TestFenceFailureKeepsExecution(t *testing.T)
func TestAuditNeverStoresTokens(t *testing.T) // dump every column of both tables: no lease token substring
func TestAuditPrunesOldestEnded(t *testing.T) // MaxAuditRows+5 ended sessions -> MaxAuditRows rows, open ones kept
func TestCommandName(t *testing.T) // "git status --short"->"git"; "/usr/bin/env FOO=1 make"->"env"; "\x01x"->"?"; 200-byte name -> 64 bytes
// internal/workspaceops/remote_test.go
func TestRemoteExecutionFencedBeforeCheckpoint(t *testing.T) // fake Fence recorded before checkpoint; after: Lease.Execution == nil, checkpoint OK
func TestRemoteExecutionFencedBeforeRelease(t *testing.T) // release OK (no history error); lease nil
func TestRemoteExecutionFencedByStopAndStopAdmin(t *testing.T)
func TestRunRefusedWhileRemoteExecution(t *testing.T) // run with token -> error, Execution still the remote one
func TestRenewAdminInvalidatesRemoteClaims(t *testing.T) // grant, stop_admin, renew_admin -> Admit(old) ErrRevoked (generation)
```

- [ ] **Step 2:** Run `go test ./internal/remotestate ./internal/remoteschema ./internal/workspaceops -run 'Remote|Grant|Admit|Fence|Audit|CommandName' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the signatures above. Reads of the audit tables run inside `Prep.Update` with a read-only body, because `Prep.Read` exposes no `*sql.Tx`.
- [ ] **Step 4:** Run `go test -race ./internal/remotestate ./internal/remoteschema ./internal/workspaceops ./internal/workspaceprep ./internal/workspacerenew ./internal/architecture`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "remote: writer sessions are the lease's remote execution, fenced first"`

---

### Task 6: The socket routes and the CLI (the call AgentPod uses in M2)

**Files:**
- Create: `internal/remoteproto/{proto.go,proto_test.go,fuzz_test.go}`, `internal/remoteapi/{handler.go,handler_test.go}`, `internal/client/remote.go`, `internal/remotecli/{cli.go,cli_test.go}`
- Modify: `internal/service/server.go` (`Config.Remote`), `internal/service/server_v2.go` (route), test `internal/service/remote_route_test.go` (budget: service ≤ 3995 after this task)
- Modify: `cmd/bench/workspace.go` (usage string gets `|remote-credential|remote-status`; one dispatch `if` placed before the `protocolv2.Known` check; budget **+3 lines**, so cmd/bench ≤ 3997)

**Interfaces:**
- Produces, **the M2 interface**. AgentPod's hub requests a writer certificate for the claiming agent's public key with:
  - `POST /v2/remote/credentials` on benchd's existing Unix socket, peer uid in `--operator-uids`, `Content-Type: application/json`, body ≤ 16 KiB:
    `{"version":"superbench.remote.v1","operation":"writer_certificate","key":"<bench key>","lease_token":"<token>","public_key":"ssh-ed25519 AAAA… comment"}`
  - `200` → `{"version":"superbench.remote.v1","operation":"writer_certificate","ok":true,"result":{"certificate":"ssh-ed25519-cert-v01@openssh.com AAAA…","user":"b-<bench id>","address":"<ip:port>","host_key":"ssh-ed25519 AAAA…","host_key_fingerprint":"SHA256:…","lease_id":"<32 hex>","generation":1,"valid_before":"<RFC 3339 UTC>"}}`
  - Errors: `{"ok":false,"error":{"code","message"}}` with `403 forbidden` (non-operator, or a token that does not own the lease), `400 invalid_request` (shape, key type), `404 not_found` (unknown bench, or remote access off), `409 conflict` (lease not usable for remote work), `503 unavailable`. Messages are static and never echo a token.
  - Calling again before `valid_before` with the same key renews: a new certificate, with the matching live connections' deadlines extended (decision 6).
  - Go client: `func client.WriterCertificate(ctx context.Context, socket string, r remoteproto.CredentialRequest) (remoteproto.Credential, error)`.
  - The contract is proposed. M2 pins it in treaty before the hub depends on it.
- Produces (`internal/remoteproto`):
  ```go
  const Version = "superbench.remote.v1"; const MaxBody = 16 << 10
  type CredentialRequest struct { Version, Operation, Key, LeaseToken, PublicKey string } // json: version, operation, key, lease_token, public_key
  type Credential struct { Certificate, User, Address, HostKey, HostKeyFingerprint, LeaseID string; Generation uint64; ValidBefore string }
  type StatusRequest struct { Version, Operation, Key string } // operation "remote_status", key optional
  type Status struct { Address, HostKeyFingerprint string; Remote []RemoteExecution; Certificates []Certificate; Sessions []Session }
  type APIError struct { Code, Message string }
  type Response struct { Version, Operation string; OK bool; Result json.RawMessage; Error *APIError }
  func ParseCredentialRequest(raw []byte) (CredentialRequest, error) // exact field set, no duplicates, strict JSON, key matches the existing key pattern, token ^[a-f0-9]{32}$
  func ParseStatusRequest(raw []byte) (StatusRequest, error)
  var ErrInvalid error
  ```
- Produces (`internal/remoteapi`):
  ```go
  type Handler struct {
      Authority *remoteca.Authority
      State     func() (remotestate.Manager, error)
      Address   string
      Operators, Readers map[uint32]bool
      Extend    func(fingerprint string, c remoteca.Claims, until time.Time) // registry hook, Task 10
      Now       func() time.Time
  }
  func (h Handler) Serve(w http.ResponseWriter, r *http.Request, uid uint32, admit func(*http.Request) error)
  ```
  The order is: parse, `Fingerprint`, `Grant`, `IssueWriter`, `RecordCertificate`, then `Extend`, then reply. A failure to record returns `503` and never the certificate.
  `POST /v2/remote/status` is open to operators and readers. It returns `remotestate.Status` (limit 50) plus the address and host-key fingerprint.
- Produces (`internal/service`), defined in service so it needs no import edge:
  ```go
  // Remote is the optional remote-access adapter; nil unless activation enables it.
  type Remote interface {
      Route(w http.ResponseWriter, r *http.Request, uid uint32, admit func(*http.Request) error)
      Serve(ctx context.Context) error
  }
  ```
  `Config.Remote Remote`. In `serveV2`, after the peer check, add `if strings.HasPrefix(r.URL.Path, "/v2/remote/") && s.config.Remote != nil { s.config.Remote.Route(w, r, uid, s.promoteNormal); return }`. `Serve` is wired in Task 11.
- Produces (`internal/remotecli`): `func Run(ctx context.Context, op string, args []string, stdout, stderr io.Writer) error`.
  - `remote-credential` takes the flags `--socket` (required), `--public-key FILE` (required), `--certificate-out FILE` (optional, written 0644) and `--token`. The token defaults to `BENCH_LEASE_TOKEN`. It prints the `Credential` JSON.
  - `remote-status` takes `--socket` and an optional key, and prints the `Status` JSON.

- [ ] **Step 1: Write the failing tests**

```go
func TestParseCredentialRequestStrict(t *testing.T) // unknown field, duplicate key, wrong version/operation, missing field, 17 KiB -> ErrInvalid
func FuzzParseCredentialRequest(f *testing.F)       // never panics; accepted requests re-marshal to the same field set
func TestCredentialRequiresOperator(t *testing.T)   // reader uid -> 403 "forbidden"; State never called
func TestCredentialIssuesCertificate(t *testing.T)  // 200; result.user == "b-"+benchID; certificate parses, KeyId == lease_id; host_key_fingerprint has prefix "SHA256:"; !bytes.Contains(body, token)
func TestCredentialRefusesBadKey(t *testing.T)      // rsa, cert-as-key, garbage -> 400 invalid_request; no Grant, no audit row
func TestCredentialRecordFailureWithholdsCertificate(t *testing.T) // RecordCertificate error -> 503, body has no "certificate"
func TestCredentialRenewalExtends(t *testing.T)     // two calls: Extend called twice, second until > first
func TestRemoteStatusOpenToReaders(t *testing.T)    // reader uid 200; no token in body
func TestRemoteRoutesAbsentWhenOff(t *testing.T)    // service with Config.Remote == nil: POST /v2/remote/credentials -> 404 "not_found"
func TestRemoteCredentialCLI(t *testing.T)          // fake socket server; BENCH_LEASE_TOKEN used; --certificate-out file holds result.certificate + "\n"
```

- [ ] **Step 2:** Run `go test ./internal/remoteproto ./internal/remoteapi ./internal/remotecli ./internal/service -run 'Credential|Remote' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** Add the architecture edges (`internal/client` + `internal/remoteproto`, `cmd/bench` + `internal/remotecli`).
- [ ] **Step 4:** Run `go test -race ./internal/remoteproto ./internal/remoteapi ./internal/remotecli ./internal/client ./internal/service ./cmd/bench ./internal/architecture`. Expected: PASS. Then run `wc -l internal/service/*.go cmd/bench/*.go`. Expected: both totals ≤ 4000.
- [ ] **Step 5: Commit** `git commit -m "remote: writer certificates and remote status on the socket"`

---

### Task 7: `bench-payload` session mode, and the benchd side that launches it

Decisions 2 and 3. The helper stays argument-free and fixed-policy. It picks the session mode only when fd 0 is a Unix stream socket. It never falls back from one frame kind to the other.

**Files:**
- Create: `internal/payloadwire/session.go` (+ `session_test.go`, `session_fuzz_test.go`)
- Create: `internal/payloadsession/{stdio_linux.go,descriptors_linux.go,namespace_linux.go,other.go}` and `{descriptors_linux_test.go,stdio_linux_test.go}`; privileged fixture `internal/payloadsession/fixture_linux_test.go` (`//go:build linux && serviceidentityfixture`)
- Create: `internal/payloadexec/session_linux.go` (budget **≤ 150 lines**, since payloadexec is at 3841; all tests live in `internal/payloadsession`)
- Modify: `cmd/bench-payload/main.go` (before `payloadexec.Exec()`: `if payloadexec.SessionStdin() { err = payloadexec.Session() }`; refusal printing unchanged)
- Create: `internal/remotesession/{launch_linux.go,launch_other.go,launch_test.go}`
- Modify: `internal/architecture/confine_test.go` (pin), `architecture_test.go` (edges, `"cmd/bench-payload"` unchanged)
- Modify: `scripts/check-identity-ci.py` `TESTS` (add the fixture and its markers)

**Interfaces:**
- Produces (`internal/payloadwire`):
  ```go
  const SessionVersion = "superbench.payload-session.v1"
  const MaxSessionCommand = 64 << 10
  type SessionRequest struct { Version, Key, LayoutID, PolicyDigest string; Scope Scope; Mode, Command, Term string } // Mode: "shell" | "exec" | "sftp"
  func (r SessionRequest) Validate() error // exec needs Command (UTF-8, no NUL, <= MaxSessionCommand); shell/sftp forbid it; Term "" or ^[A-Za-z0-9._+-]{1,64}$ and never with sftp; ids as Request.Validate
  func (r SessionRequest) Bytes() ([]byte, error)
  func ReadSessionFrame(input *os.File, timeout time.Duration) (SessionRequest, error) // same 4-byte BE length framing; a payload.v1 frame -> ErrFrame
  ```
- Produces (`internal/payloadsession`):
  ```go
  func VerifyStdio(daemonUID uint32) error // fd0: S_IFSOCK, AF_UNIX, SOCK_STREAM, SO_PEERCRED uid == daemonUID; fd1, fd2: FIFO O_WRONLY owned by daemonUID
  func Receive(control int, want int, timeout time.Duration) ([]*os.File, error)
  func VerifyDescriptors(fds []*os.File, pty bool, daemonUID uint32) error // pty: 1 fd, S_IFCHR major 136..143, O_RDWR; else 3: FIFO RDONLY, FIFO WRONLY, FIFO WRONLY; owner daemonUID
  func Enter(p payloadpolicy.Policy, key string, bench *os.File) error       // private mount namespace, see Step 3
  var ErrSession error
  ```
- Produces (`internal/payloadexec`): `func SessionStdin() bool` (fstat fd 0 is `S_IFSOCK`) and `func Session() error`. Fixed executables: shell `/usr/bin/bash` (argv `["bash","-l"]`), exec (`["bash","-c",Command]`), sftp `/usr/local/libexec/bench-sftp` (argv `["bench-sftp"]`). Each is verified root-owned, regular, not group or world writable, and without setuid, like `verifyFixedGit`.
- Produces (`internal/remotesession`):
  ```go
  type Installed struct{} // resolves serviceidentity on every Start; no test can inject a helper path
  func (Installed) Start(ctx context.Context, a remotestate.Admission, spec remotessh.SessionSpec, fds []*os.File) (remotessh.Process, error)
  var ErrRefused error // wraps the helper's refusal code read from its stderr (<= 4 KiB)
  ```
  Start builds `SessionRequest` from `a` (`LayoutID = a.LayoutID`, `Scope = a.Execution`, `PolicyDigest` from `serviceidentity.ForPolicy`). It creates `socketpair(AF_UNIX, SOCK_STREAM|SOCK_CLOEXEC)` and stdout/stderr pipes. It runs `/usr/bin/sudo -n -- /usr/local/libexec/bench-payload` with `Dir "/"`, a fixed `PATH` plus `payloadexec.AmbientEnvironment`, and stdin set to the child socket end, started through `execution.Host{}.Start(cmd, &a.Execution)` so the helper is born in the remote scope. Then it writes the frame, then `Sendmsg(parent, []byte{'F'}, syscall.UnixRights(fds...))`, then `Shutdown(SHUT_WR)`, closes its copies, and waits ≤ 30 s for `payloadwire.ExecReached` on the socket. If EOF comes without the marker, it returns `ErrRefused`.
  It consumes `remotessh.SessionSpec` and `remotessh.Process`, which Task 10 defines. Write those two types first, in `internal/remotessh/launcher.go`, as part of this task.

- [ ] **Step 1: Write the failing tests**

```go
func TestSessionRequestValidate(t *testing.T)         // table of the rules above
func TestFramesNeverCrossKinds(t *testing.T)          // ReadSessionFrame(payload.v1 frame) -> ErrFrame; ReadAnyInvocation(session frame) -> ErrFrame
func FuzzParseSessionRequest(f *testing.F)
func TestReceiveExactDescriptorCount(t *testing.T)    // 3 sent, want 3 -> ok; 2 or 4 -> refused and every received fd closed (count /proc/self/fd before/after)
func TestReceiveRefusesTruncationAndExtraData(t *testing.T) // 8 fds into a 3-fd buffer (MSG_CTRUNC), data byte != 'F', bytes after 'F' before EOF -> refused, no fd leak
func TestVerifyDescriptorsRefusesWrongKinds(t *testing.T) // /dev/null as pty, reversed pipe ends, a socket -> refused
func TestVerifyStdioRefusesPipeStdin(t *testing.T)
func TestInstalledStartRefusesWithoutBinding(t *testing.T) // no installed policy -> error, no process started
// architecture/confine_test.go
func TestSessionReachesPayloadUIDOnlyThroughDropCredentials(t *testing.T) // session_linux.go: exactly one "dropCredentials(" and "payloadsession.Enter(" occurs before it
// privileged fixture (root, Linux): TestInstalledRemoteSessionFixture prints, each only on success:
//   SESSION_NAMESPACE_SHOWS_ONLY_ITS_BENCH   (ls <DataRoot> == [benches]; ls <DataRoot>/benches == [key]; <StateRoot> empty; a second bench's file absent)
//   SESSION_TMPFS_READ_ONLY                  (touch <DataRoot>/x -> EROFS)
//   SESSION_CANNOT_UNMOUNT_TO_REVEAL         (unshare -Urm umount <DataRoot> fails, or the other bench stays absent)
//   SESSION_PAYLOAD_UID_AND_SECCOMP          (id -u == PayloadUID; /proc/self/status "Seccomp:\t2")
//   SESSION_STAYS_IN_REMOTE_SCOPE            (/proc/self/cgroup names the remote execution's scope)
//   SESSION_PTY_CONTROLLING_TERMINAL         (tty -> /dev/pts/N owned by PayloadUID, mode 0600)
//   SESSION_DESCRIPTOR_REFUSALS_NO_EXEC      (wrong count/type -> refusal code on stderr, ExecReached never written)
```

- [ ] **Step 2:** Run `go test ./internal/payloadwire ./internal/payloadsession ./internal/remotesession ./internal/architecture -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** `Enter` follows exactly this order. Nothing is resolved by following a name an unprivileged user can change. `open_tree` is 428 and `move_mount` is 429 on both shipped architectures; call them as raw syscalls, as `close_range` is called today.

```text
1  unshare(CLONE_NEWNS); mount(NULL, "/", NULL, MS_REC|MS_PRIVATE, NULL)
2  clone = open_tree(bench, "", OPEN_TREE_CLONE|OPEN_TREE_CLOEXEC|AT_EMPTY_PATH)   // bench = the pinned, verified workspace-root fd
3  for root in (p.DataRoot, p.StateRoot):
       mount("tmpfs", root, "tmpfs", MS_NOSUID|MS_NODEV|MS_NOEXEC, "mode=0755,size=64k,nr_inodes=64")
4  d = open(p.DataRoot, O_PATH|O_DIRECTORY|O_NOFOLLOW); mkdirat(d, "benches", 0755); b = openat(d, "benches", O_PATH|O_DIRECTORY|O_NOFOLLOW); mkdirat(b, key, 0755)
5  move_mount(clone, "", b, key, MOVE_MOUNT_F_EMPTY_PATH)
6  mount(NULL, <DataRoot>/benches/<key>, NULL, MS_REMOUNT|MS_BIND|MS_NOSUID|MS_NODEV, NULL)
7  for root in (p.DataRoot, p.StateRoot): mount(NULL, root, NULL, MS_REMOUNT|MS_RDONLY|MS_NOSUID|MS_NODEV|MS_NOEXEC, NULL)
8  verify: readdir(DataRoot)==["benches"], readdir(DataRoot/benches)==[key], stat(DataRoot/benches/key) dev+ino == fstat(bench), readdir(StateRoot)==[] ; else ErrSession
```

`Session()` runs its phases in this order. Each one refuses with the existing `refusalError` phase names:
1. caller is root
2. `loadHelperBinding`
3. `payloadsession.VerifyStdio`
4. `descriptorsCloseOnExec`
5. `ReadSessionFrame`
6. build `r := Request{Kind:"payload", Root:true, Key, LayoutID, PolicyDigest, Scope, Arguments:<fixed argv>}`
7. `VerifyScope`, `LoadLayout`, `openBoundary`
8. `Receive(0, n)`, then `VerifyDescriptors`
9. `payloadsession.Enter(p, key, b.cwd)`
10. re-check the layout, boundary, scope and binding exactly as `Exec` does
11. `Environment(home, tmp, …)`, plus `TERM` when a PTY is requested
12. verify the fixed executable
13. for a PTY, `fchown(slave, PayloadUID, PayloadGID)` and `fchmod 0600`
14. move the control socket to fd ≥ 10 with `F_DUPFD_CLOEXEC`, then `dup3` the session fds onto 0–2 and close the originals
15. `dropCredentials(p)`
16. `setsid()`, and for a PTY `ioctl(0, TIOCSCTTY, 0)`
17. `chdir("<DataRoot>/benches/<key>")`, checking that its dev and ino match the pinned fd
18. `Clearenv` and set the environment
19. write `ExecReached` to the control fd
20. `syscall.Exec`

- [ ] **Step 4:** Run `go test -race ./internal/payloadwire ./internal/payloadsession ./internal/remotesession ./internal/payloadexec ./internal/architecture` and `GOOS=linux CGO_ENABLED=0 go build ./cmd/bench-payload`. Expected: PASS. Then on the staging host run `python3 scripts/check-ci.py --privileged-only`. Expected: every `SESSION_*` marker and `--- PASS: TestInstalledRemoteSessionFixture`.
- [ ] **Step 5: Commit** `git commit -m "payload: argument-free session mode in a private mount namespace"`

---

### Task 8: `bench-sftp`, the SFTP server confined to the workspace root

`pkg/sftp` stays out of the privileged helper: package init code would run as root there. `bench-sftp` is a separate executable that `bench-payload` execs after the credential drop, inside the session's namespace.

**Files:**
- Create: `internal/remotesftp/{handlers.go,handlers_test.go}`, `cmd/bench-sftp/main.go`
- Modify: `go.mod`/`go.sum` (`github.com/pkg/sftp`, exact), the architecture exception (`github.com/pkg/sftp` only in `internal/remotesftp`), `deploy/linux/install.py` (install `usr/local/libexec/bench-sftp`, 0755 root, in `EXECUTABLES` with the reviewed-digest check), `deploy/linux/install_test.py`, the release build (`scripts/release.sh`, `.github/workflows/release.yml`), `THIRD_PARTY_NOTICES.md`

**Interfaces:**
- Produces: `func remotesftp.Serve(rw io.ReadWriteCloser, root *os.Root) error` and `func remotesftp.RefuseRoot(uid int) error`. `cmd/bench-sftp` is argument-free (any argument exits 2), refuses uid 0, opens `os.OpenRoot(".")`, and serves stdin/stdout.
- Path mapping: an SFTP path is `path.Clean("/"+p)`, `"/"` is the workspace root, and every operation goes through `os.Root`. `Getwd` and `realpath(".")` return `"/"`. Refused (`sftp.ErrSSHFxPermissionDenied`): chown, hard links, and `Setstat` of uid or gid. Allowed: read, write, create, truncate, mkdir, rmdir, remove, rename, symlink (target stored verbatim, never followed out of the root), readlink, chmod, chtimes, list and stat.

- [ ] **Step 1: Write the failing tests** (an in-process client over `net.Pipe` through `sftp.NewClientPipe`)

```go
func TestSFTPRootedAtWorkspace(t *testing.T)     // Getwd()=="/"; ReadDir("/") == member dirs; read/write/rename inside OK
func TestSFTPRefusesEscapes(t *testing.T)        // "../x", "/../../etc/passwd", "member/../../x" -> not found or permission denied; nothing outside the temp root changes
func TestSFTPRefusesSymlinkEscape(t *testing.T)  // Review Focus 4: root has link -> /etc; Open("link/passwd") refused; Symlink("/etc","l2") stored, Open("l2/passwd") refused
func TestSFTPRefusesChownAndLink(t *testing.T)
func TestSFTPWriteFailureKeepsServing(t *testing.T) // Review Focus 5: write into a 0500 dir -> SFTP failure status; next Stat succeeds on the same client
func TestRefuseRoot(t *testing.T)                // RefuseRoot(0) error, RefuseRoot(1000) nil
```

- [ ] **Step 2:** Run `go test ./internal/remotesftp -v`. Expected: FAIL.
- [ ] **Step 3: Implement** with `sftp.NewRequestServer` and handlers over `*os.Root`.
- [ ] **Step 4:** Run `go test -race ./internal/remotesftp ./internal/architecture`, `python3 -m unittest discover -s deploy/linux -p '*_test.py'` and `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/...`. Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "remote: bench-sftp serves only the workspace root"`

---

### Task 9: Security review checkpoint, session mode (gate)

No code. Task 10 does not start until this gate passes.

**Files:** none changed. Findings go in the PR description, and fixes go in follow-up commits on Tasks 7–8.

- [ ] **Step 1:** Dispatch a fresh reviewer, on the most capable model, with only these inputs: spec decisions 2 and 3, `internal/payloadexec/{exec_linux.go,session_linux.go,credentials_linux.go,descriptors_linux.go}`, `internal/payloadsession`, `internal/payloadwire/session.go`, `internal/remotesession`, `internal/remotesftp`, `cmd/bench-sftp`, `cmd/bench-payload`, `deploy/linux/superbench.sudoers`, and the confine pins.
- [ ] **Step 2:** The reviewer answers each item yes or no, with file:line:
  1. Can any frame or descriptor make the helper exec anything but the three fixed argvs?
  2. Is every privileged step before the credential drop either fixed-policy or verified?
  3. Can a received fd, or a leftover, reach the payload other than as fd 0–2?
  4. Do steps 2–8 of `Enter` leave any other bench, the state root or another bench's runtime reachable by path or by an inherited fd?
  5. Can the payload undo the mounts (user namespaces, `MNT_LOCKED`)?
  6. Is the seccomp filter installed in session mode (`Seccomp: 2`)?
  7. Does a refusal ever write `ExecReached`?
  8. Does `bench-sftp` follow any path out of the root?
  9. Does `sudo -n` with `!use_pty` pass the socket stdin through unchanged?
- [ ] **Step 3:** Run `python3 scripts/check-ci.py --privileged-only` on the staging host. Expected: PASS with every `SESSION_*` marker. Attach the log to the PR.
- [ ] **Step 4:** Any "no" is fixed and re-reviewed before Task 10.

---

### Task 10: The SSH server

**Files:**
- Create: `internal/remotessh/{launcher.go (from Task 7),server.go,conn.go,session.go,pty_linux.go,pty_other.go,registry.go}` and tests `{auth_test.go,channel_test.go,registry_test.go,fuzz_test.go,openssh_linux_test.go}`
- Create: `internal/remotessh/remotesshtest/local.go`
- Modify: the architecture exception (`golang.org/x/crypto/ssh` for `internal/remotessh`), edges

**Interfaces:**
- Consumes: `remoteca.Authority.Verify/HostSigner` (Task 4), `remotestate.Admission` and `remoteca.Claims` (Tasks 4–5), `remotestate.Manager.SessionStarted/SessionEnded` (Task 5).
- Produces:
  ```go
  type SessionSpec struct { Mode, Command, Term string; PTY bool; Cols, Rows uint32 }
  type Process interface { Wait() (exitCode int, signal string, err error); Terminate() } // Terminate: SIGTERM, then SIGKILL after 5 s
  type Launcher interface { Start(ctx context.Context, a remotestate.Admission, spec SessionSpec, fds []*os.File) (Process, error) }
  type Audit interface { SessionStarted(a remotestate.Admission, r remoteschema.SessionRecord) (int64, error); SessionEnded(id int64, in, out uint64, reason string) error }
  type Config struct {
      Authority *remoteca.Authority
      Admit     func(remoteca.Claims) (remotestate.Admission, error)
      Launcher  Launcher
      Audit     Audit
      Registry  *Registry
      Now       func() time.Time
  }
  const ( HandshakeTimeout = 10 * time.Second; MaxAuthTries = 3; MaxUnauthenticated = 16; MaxConnections = 64; MaxSessionsPerBench = 8; SweepInterval = time.Second; ServerVersion = "SSH-2.0-SuperBench" )
  func Serve(ctx context.Context, l net.Listener, c Config) error
  func HandleConn(ctx context.Context, nc net.Conn, c Config) // exported for the fuzz tests
  type Registry struct{ /* conns by id: bench id, key fingerprint, claims, deadline timer */ }
  func NewRegistry() *Registry
  func (r *Registry) Extend(fingerprint string, c remoteca.Claims, until time.Time) // every conn with this fingerprint and claims
  ```
  `remotessh` imports `internal/remoteschema` for `SessionRecord`; add that edge. `remotesshtest.Local{}` implements `Launcher`: `/bin/sh -l` or `-c` as the caller in `a.Root`, and SFTP through `remotesftp.Serve` in process.
- Behaviour:
  - `ssh.ServerConfig` sets only `PublicKeyCallback` (`Verify`, then `Admit`; the claims and fingerprint go in `Permissions.Extensions`), with `MaxAuthTries` and `ServerVersion`. There is no password, keyboard-interactive or `NoClientAuth`.
  - After authentication, a deadline timer closes the connection at `cert.ValidBefore`.
  - `ssh.DiscardRequests` handles global requests (`tcpip-forward` gets a false reply).
  - Non-`session` channels get `Reject(Prohibited)`.
  - On each `session` channel open, `Admit` runs again and the per-bench limit is checked. Failure gets `Reject(Prohibited)` or `ResourceShortage`.
  - Requests:
    - `pty-req`: term must match the pattern, and cols and rows ≤ 1000.
    - `window-change` uses `TIOCSWINSZ` on the master.
    - `shell`, `exec` (≤ 64 KiB, UTF-8, no NUL) and `subsystem` `sftp` are accepted once per channel.
    - `env`, `x11-req`, `auth-agent-req@openssh.com`, `signal` and any other request get a false reply.
  - PTY: open `/dev/ptmx` (`O_RDWR|O_NOCTTY|O_CLOEXEC`), unlock it, and take the slave with `TIOCGPTPEER` (0x5441). It is raw `ioctl` through `syscall`, with no `x/sys`.
  - Exit sends `exit-status` or `exit-signal`, then closes the channel.
  - Bytes in each direction are counted for the audit.
  - The sweep re-runs `Admit` every `SweepInterval` for every connection. On refusal, it closes the connection, calls `Terminate` on its processes and ends their audit rows with `revoked`. Deadline expiry ends them with `expired`.

- [ ] **Step 1: Write the failing tests** (`x/crypto/ssh` client in-process; fake `Admit`; `remotesshtest.Local`)

```go
func TestCertificateOnlyAuth(t *testing.T)       // password, keyboard-interactive, plain key -> handshake error; Admit never called
func TestRefusedCertificates(t *testing.T)       // wrong bench user, expired (Now past ValidBefore), other CA, Admit -> ErrRevoked (old generation): handshake error each
func TestChannelRecheck(t *testing.T)            // Admit flips to revoked after auth -> session channel rejected; Launcher.Start not called
func TestForwardingAndExtrasRefused(t *testing.T) // direct-tcpip rejected; tcpip-forward false; auth-agent-req, x11-req, env, subsystem "foo" false
func TestOneStartPerChannel(t *testing.T)        // second exec on the same channel false; Start called once
func TestSessionLimitPerBench(t *testing.T)      // 9th session -> ResourceShortage
func TestDeadlineClosesConnection(t *testing.T)  // ValidBefore = now+2s -> conn closed within 3s; audit "expired"
func TestExtendKeepsConnection(t *testing.T)     // Registry.Extend before deadline -> open after original deadline
func TestSweepClosesRevoked(t *testing.T)        // Admit flips -> conn closed within 2s; running "sleep 60" terminated; audit "revoked"
func TestStockOpenSSHClient(t *testing.T)        // Linux; requires ssh, scp, sftp on PATH (t.Fatal if missing and CI=true):
//   ssh -F /dev/null -i key -o CertificateFile=key-cert.pub -o IdentitiesOnly=yes -o IdentityAgent=none
//       -o UserKnownHostsFile=kh -o StrictHostKeyChecking=yes -o BatchMode=yes -p PORT b-<id>@127.0.0.1 'echo hi; pwd'
//       -> stdout "hi\n<root>\n", exit 0
//   ssh -tt ... tty -> stdout matches ^/dev/pts/\d+
//   sftp -b batch (put, get, ls, cd ..) -> files under root only; "cd .." stays at "/"
//   scp local b-<id>@127.0.0.1:member/f -> lands at <root>/member/f
//   ssh -N -o ExitOnForwardFailure=yes -L 0:127.0.0.1:9 ... -> non-zero exit
//   wrong host key in kh -> non-zero exit, Launcher never called
func FuzzHandshake(f *testing.F)       // random client bytes over net.Pipe: HandleConn returns within 1s, never calls Admit or Start
func FuzzSessionRequests(f *testing.F) // authenticated conn; fuzzed (request type, payload) sequence: no panic, Start only for valid shell/exec/sftp, at most once per channel
```

- [ ] **Step 2:** Run `go test ./internal/remotessh/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `go test -race ./internal/remotessh/... ./internal/architecture`, then `go test ./internal/remotessh -run '^$' -fuzz FuzzHandshake -fuzztime 120s` and the same for `FuzzSessionRequests` (local, not CI). Expected: PASS, no crashers.
- [ ] **Step 5: Commit** `git commit -m "remote: certificate-only SSH server with exec, PTY and SFTP"`

---

### Task 11: Wire it into `benchd`, with end-to-end ordinary tests

**Files:**
- Create: `internal/remoteaccess/{access.go,access_test.go,fixture_test.go,e2e_linux_test.go,off_linux_test.go}`
- Modify: `internal/service/server.go` (in `serve`, after receipts open: `if c.Remote != nil { go func(){ remoteDone <- c.Remote.Serve(lifetime) }() }`; a remote error cancels `lifetime`; shutdown waits on `remoteDone`; budget: service ≤ 4000)
- Modify: `cmd/benchd/main.go` (`if switches.RemoteAccess { remote, err = remoteaccess.New(store, switches) }`, then set `Config.Remote`), `cmd/benchd/activation_test.go`

**Interfaces:**
- Consumes: everything from Tasks 4–10.
- Produces:
  ```go
  type Access struct{ /* store, address, authority (opened lazily in Serve), registry, launcher */ }
  func New(store bench.Store, s activation.Switches) (*Access, error) // refuses unless s.RemoteAccess; opens nothing
  func (a *Access) Serve(ctx context.Context) error // RequireOperationState; remoteca.Open(<state>/remote); net.Listen("tcp", s.RemoteFleetAddress.String()); remotessh.Serve(...)
  func (a *Access) Route(w http.ResponseWriter, r *http.Request, uid uint32, admit func(*http.Request) error) // remoteapi.Handler; 503 until Serve has opened the authority
  ```
  `Access` satisfies `service.Remote`. The launcher is `remotesession.Installed{}`. Tests build it with an unexported option for `remotesshtest.Local`, fake `Scopes` and a pre-bound `net.Listener`.

- [ ] **Step 1: Write the failing tests** (`fixture_test.go` builds a leased v2 workspace on a local bare remote, as in `workspaceops/manager_test.go:fixture`)

```go
func TestOffStateOpensNoPortAndNoKey(t *testing.T) // Linux: benchd run() with switches off -> once the socket answers, no LISTEN entry in /proc/self/net/tcp{,6} whose inode is one of /proc/self/fd's sockets; <state>/remote does not exist; remoteaccess.New not called
func TestBenchdRefusesRemoteWithoutFresh(t *testing.T) // cmd/benchd: activation with remote_access, no fresh_payload -> error, no socket created
func TestRemoteAccessEndToEnd(t *testing.T)       // stock ssh: issue via Route (operator uid) -> ssh 'echo ok' -> "ok"
func TestCheckpointCutsLiveSession(t *testing.T)  // ssh 'sleep 30' running -> workspaceops checkpoint -> ssh exits non-zero within 2s; audit "fenced"; same cert again -> refused
func TestReleaseCutsLiveSession(t *testing.T)
func TestStopCutsLiveSession(t *testing.T)
func TestSweepClosesConnectionAfterRenewAdmin(t *testing.T) // Review Focus 1: idle `ssh -N` conn; stop_admin + renew_admin -> conn closed within 2s; old cert refused (old generation)
func TestWrongBenchCertificateRefused(t *testing.T) // cert for bench A as user b-<B> and as b-<A> on reclaimed A -> refused
func TestExpiredCertificateRefused(t *testing.T)    // IssueWriter with now-40m -> refused
func TestAbruptDisconnectThenReleaseFencesLeftovers(t *testing.T) // Review Focus 2: ssh 'nohup sleep 60 & echo $!' then kill -9 the ssh client -> audit "closed"; release -> fake Scopes.Fence called, leftover pid gone
func TestRenewalOverSocketKeepsConnection(t *testing.T) // short validity via test clock; second Route call before expiry -> conn alive past first ValidBefore
```

- [ ] **Step 2:** Run `go test ./internal/remoteaccess ./cmd/benchd -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `sh scripts/check.sh`. Expected: PASS, including `go vet` with zero warnings and every build. Then run `wc -l internal/service/*.go | tail -1`. Expected: ≤ 4000.
- [ ] **Step 5: Commit** `git commit -m "benchd: the fleet SSH listener, only when remote_access is on"`

---

### Task 12: Security review checkpoint, the SSH door (gate)

**Files:** none changed. Findings go in the PR description.

- [ ] **Step 1:** Dispatch a fresh reviewer, on the most capable model, with spec §1–§3, §8, decisions 4–7, `internal/remoteca`, `internal/remotestate`, `internal/remoteapi`, `internal/remotessh`, `internal/remoteaccess` and the Task 5 workspaceops change.
- [ ] **Step 2:** The reviewer answers each item yes or no, with file:line:
  1. Is the lease checked at auth, at channel open and by the sweep, against ID, generation and execution?
  2. Can any channel type or request open a forward, an agent or X11?
  3. Can a certificate for one bench reach another?
  4. Do checkpoint, release, stop and stop_admin always fence before proceeding, and does a fence failure stop them?
  5. Is anything listening, or any key file present, with `remote_access` off?
  6. Can a token reach a log, an audit row or a response?
  7. Are the unauthenticated, connection and session limits enforced before any allocation proportional to input?
  8. Does a panic in one connection's goroutine take down `benchd`? (Each connection goroutine recovers and closes.)
- [ ] **Step 3:** Fix any "no" before Task 13.

---

### Task 13: Privileged drill on the staging host, and the CI lane

**Files:**
- Create: `scripts/test-remote-access.py` (disposable-host drill in the style of `scripts/daemon-integration.py`: takes the installed host, creates its own workspace on a throwaway key, never touches live benches), `docs/remote-access-output.txt` (captured output)
- Modify: `scripts/check-identity-ci.py` (fixture already added in Task 7; confirm it runs in `--privileged-only`)

- [ ] **Step 1:** The drill asserts, each printing a `PASS:` line:
  - `ssh … id -u` equals the payload uid
  - `ls <data>/benches` shows only the workspace's key
  - `cat /proc/self/cgroup` is under the lease's remote scope
  - release cuts a running `sleep 60` within 2 s (`ssh` exits non-zero) and its scope directory is gone
  - stop and stop_admin cut sessions the same way
  - certificates for the wrong bench, expired certificates and old-generation certificates are refused
  - after restarting `benchd` with `remote_access` off on fresh state, `ss -ltnp` lists no `benchd` TCP socket and `<state>/remote` is absent
- [ ] **Step 2:** On the staging host, run `sudo python3 scripts/test-remote-access.py --bench /usr/local/bin/bench --socket /run/superbench/benchd.sock > docs/remote-access-output.txt`. Expected: every `PASS:` line and exit 0.
- [ ] **Step 3: Commit** `git commit -m "remote: privileged drill and its recorded output"`

---

### Task 14: Docs, dependency policy and CHANGELOG

**Files:**
- Modify: `docs-site/src/content/docs/start/security-model.md`: "No TCP listener" becomes "No TCP listener unless remote access is enabled" (decision 11). The paragraph names the off-state test and the drill.
- Modify: `docs-site/scripts/honesty.mjs` CAVEATS `start/security-model.md`: replace `'no test asserts the absence of a TCP socket'` with `'no TCP listener unless remote access is enabled'`, which is the new SB-08 wording (see Report gap 5).
- Create: `docs-site/src/content/docs/concepts/remote-access.md`, with front matter `title: Remote access`, a description and `status: wet paint`. It covers:
  - off by default
  - `remote_access` requires `fresh_payload`
  - the fleet listener address
  - certificates: 30 minutes, `b-<bench id>`, lease ID and generation, renewal over the socket
  - what a session can do, and what is refused
  - "sessions are the lease's remote execution", so checkpoint, release, stop and stop_admin cut them
  - the mount namespace shows only that bench. State it without the word "isolation" except negated: "this is not isolation of untrusted code"
  - egress is unchanged from supervised runs, which have no network isolation today (decision 11)
  - the audit and `bench workspace remote-status`
  - an example `ssh` command, using a generic harness name only
  - what is not built yet: watchers, the key list, OIDC, the public listener
- Modify: `docs-site/astro.config.mjs` (sidebar entry after "Payload identity and the fixed helpers"), `docs-site/src/content/docs/reference/bench.md` (workspace `remote-credential`, `remote-status`), `docs-site/src/content/docs/concepts/payload-identity.md` (session mode and bench-sftp; see gap 5 on its caveat)
- Modify: `docs/pilot-installation.md` (activation table rows `remote_access`, `remote_fleet_address`, dependency row "requires `fresh_payload`", and the schema-31 upgrade step: start once with activation off to migrate, because an activated daemon never migrates), `docs/code-quality.md` (the two new pinned imports and where they are allowed), `AGENTS.md` (external runtime imports sentence), `THIRD_PARTY_NOTICES.md`
- Modify: `CHANGELOG.md` `## Unreleased`: remote access (off by default; what it needs; the upgrade step for schema 31; `bench-sftp` is a new installed executable)
- Modify: `.forgejo/workflows/ci.yml` docs path filter (add `internal/remotecli/` and `internal/activation/` only if check-claims is extended to read them)

- [ ] **Step 1:** Write the pages.
- [ ] **Step 2:** Run `cd docs-site && npm ci --no-audit --no-fund && npm run build`. Expected: PASS (claims, honesty, copy, links). Then run `sh scripts/check.sh`. Expected: PASS.
- [ ] **Step 3: Commit** `git commit -m "docs: remote access, and no TCP listener unless it is enabled"`

---

## Later milestones

- **M2:** AgentPod hub and Hermes wiring. The hub calls `POST /v2/remote/credentials` (`client.WriterCertificate`) for the claiming agent's public key, after M2 pins that contract in treaty. Hermes gets its SSH backend config. The decision-10 spike comes first.
- **M3:** watchers and grants. Read-only certificates, `bench-payload` read-only bind, and per-bench, board or card grants with the hub's ceiling.
- **M4:** the operator key list, a credential path for remote callers with no socket access.
- **M5:** OIDC issuers mapped onto the same grants.
- **M6:** the public listener (`remote_access_public`), with per-source rate limits on failed authentication.
- **Separate project:** reusable benches (per-repo templates and warm pools).
