# Fresh Linux installation and checkpoint pilot configuration

This is the installation and configuration handoff for a supervised staging
rehearsal. The coordinator-grant design is accepted, subject to its gates before
enablement. Acceptance does not provision a service principal or prove a working
pilot. SuperBench remains standalone; the suite calls it, and SuperBench does not
call suite products. All suite checkpoint switches remain off by default.

**Current boundary:** on a managed host the installer creates the separate
`superbench-payload` account, the private payload policy and the public service
identity the payload helper requires, and an activation file with every v2 switch
off. benchd turns on the Fresh payload layout, finite I/O, receipt lookup,
cancellation and registration observation only from that root-owned file (see
[Activation](#activation)). With the file absent or all switches off, the daemon
behaves exactly as before. Do not make Node-private worker contexts readable to the
payload account to work around an adapter refusal. Installation success and matching
hashes are not evidence of a successful card or writer fencing.

## Current gaps

The following must be closed or verified before the rehearsal can establish an
end-to-end card result:

- **No activated run on a booted host yet.** Creating a Fresh bench and running a
  real task through the payload helper, with all five switches on, is covered by
  tests and fixtures but has not been run on an installed, booted host.
- **Harness configuration in the payload home.** The payload helper sets `HOME`
  from each bench's Fresh layout. Nothing yet places a harness's provider
  configuration there, and the harness adapter refuses to provision credentials.
  This needs a design decision (a shared read-only profile directory, a bench
  setup step or a keyless provider endpoint) before a provider-backed card can run.
- **Fresh recovery stays manual.** An interrupted or cancelled Fresh bootstrap is
  recovered by the direct-root steps in
  [Retained Fresh bootstrap evidence](fresh-bootstrap-recovery.md); a
  `transferring` tree stays parked. Turn `fresh_payload` on only with those steps
  at hand. Changing modes cannot adopt an incomplete legacy checkout.
- **AgentPod release and pins.** The Node needs an operator-approved AgentPod
  release that contains the coordinator wiring, the harness adapter and a
  compatible worker. Re-pin the installed Node and worker binaries and the registry
  after that upgrade (section 2).

Keep checkpoint flags off until the enablement gates and actual lifecycle checks
pass. Configuration, matching hashes and a successful install are not evidence
that a card can complete.

## 1. Prepare and install the standalone service

Use a fresh, disposable staging Linux host with systemd, delegated cgroup v2,
Python 3, Git and sudo. Managed storage requires XFS tools and a separately mounted
XFS filesystem with enforcing project quotas. Keep SQLite state and the quota
ledger outside the bounded data filesystem. The installer neither formats nor
mounts filesystems. Use the same architecture for the build and target host.

From the reviewed SuperBench source revision, with Go 1.26.4:

```sh
mkdir -p /absolute/build/bin
GOMAXPROCS=8 go build -p 8 -o /absolute/build/bin/bench ./cmd/bench
GOMAXPROCS=8 go build -p 8 -o /absolute/build/bin/benchd ./cmd/benchd
GOMAXPROCS=8 go build -p 8 -o /absolute/build/bin/bench-quota ./cmd/bench-quota
CGO_ENABLED=0 GOMAXPROCS=8 go build -p 8 -o /absolute/build/bin/bench-payload ./cmd/bench-payload
python3 deploy/linux/install.py --bin-dir /absolute/build/bin \
  --root /absolute/private/staging --managed
```

These are the four binaries required by the managed installer. The payload
helper must be a static Linux executable for the target architecture; do not
omit it or substitute a dynamically linked build. Review the staged files,
owners, modes, systemd unit, wrapper and sudo rule before applying anything.
Staging writes only below the supplied physical absolute root. On a fresh host,
direct Linux root then runs:

```sh
python3 deploy/linux/install.py --bin-dir /absolute/build/bin --root / \
  --managed --prepare --identity --activation
```

This prepares the stable `superbench` account, the separate `superbench-payload`
system account (nologin, its own group, no members) and the directories, and
reloads systemd; it does not start or enable the service. Existing conflicting
files, unsafe paths and an active installation refuse. This is not an upgrade or
legacy-adoption procedure. See [installer details](install.md) for preflight and
recovery.

`--identity` then writes two root-owned documents from the accounts' actual
numeric IDs:

| File | Mode | Content |
| --- | --- | --- |
| `/etc/superbench/payload-policy.json` | 0600 | `{"DaemonUID":…,"DaemonGID":…,"PayloadUID":…,"PayloadGID":…,"DataRoot":"/srv/superbench-data","StateRoot":"/var/lib/superbench/state","CgroupRoot":"/sys/fs/cgroup/system.slice/superbench.service"}` |
| `/etc/superbench/service-identity.json` | 0644 | `{"Version":"superbench.service-identity.v1","LayoutVersion":"superbench.payload-layout.v1","Policy":{…the same policy…},"PolicySHA256":"…"}` |

Both are the canonical compact JSON the helper and the daemon verify; the
registry's `PolicySHA256` is the SHA-256 of the exact policy bytes. `CgroupRoot`
must be the cgroup the unit is delegated, which `superbench-start` passes to
benchd: `/sys/fs/cgroup` followed by `systemctl show -p ControlGroup --value
superbench.service`. Pass `--cgroup-root` if the unit runs elsewhere. The accounts
must exist (they do after `--prepare`); identity documents found without them,
or documents that differ from what the accounts produce, refuse rather than being
replaced. To change one, remove it deliberately and review the new file.

`--activation` writes `/etc/superbench/activation.json` with every switch off.
Rerunning the installer accepts identical files and refuses changed ones, so do
not pass `--activation` again after editing the file.

Write root-owned `/etc/superbench/storage.json` using the actual numeric
`id -u superbench` and `id -g superbench`, the dedicated data mount and reviewed
byte **and** inode budgets. Use [the storage policy example](storage.md), replacing
its example IDs and sizes. Preserve reserve space; do not recursively chown the
volume or delete a ledger to bypass refusal. Configure fresh state as the service
account:

```sh
sudo -u superbench /usr/local/bin/bench configure-host \
  --state /var/lib/superbench/state --data /srv/superbench-data \
  --quota-helper /usr/local/libexec/bench-quota \
  --min-data-free 1073741824 --min-state-free 536870912
```

Edit root-owned `/etc/superbench/benchd.env`:

```ini
BENCHD_OPERATOR_UIDS=0
BENCHD_READER_UIDS=
BENCHD_MAX_ACTIVE=8
BENCHD_RETENTION_INTERVAL=1m
BENCHD_MIRROR_TIMEOUT=2m
```

Add the actual Node process UID to the comma-separated operator allowlist when
it is authorized to mutate benches. Separately give that process filesystem
access through the service group. The socket parent is 0750 and socket 0660;
group membership alone grants no API authority. The daemon UID is not implicitly
an operator. Keep daemon state private. Supply repository credentials through the
approved service-account credential mechanism, never a worker prompt, repository,
tracked environment file or Node handoff.

The rehearsal operator starts the service explicitly:

```sh
systemctl enable --now superbench.service
/usr/local/bin/bench host-status --socket /run/superbench/benchd.sock
systemctl status superbench.service
journalctl -u superbench.service --no-pager
```

The installed wrapper discovers the delegated cgroup from the running service.
Keep `Delegate=yes` and the reviewed managed helper boundary. Do not change the
unit's privilege restrictions independently of the helper installation. Require
an actual Linux managed host-status response; service activity alone is not
managed readiness or proof that a previous execution has stopped.

### Activation

benchd reads the default-off v2 switches only from
`/etc/superbench/activation.json`. No request, environment variable, `benchd.env`
entry or command-line flag can set them. The file is a JSON object with the exact
version and any of five switches, each a literal `true` or `false`:

```json
{
  "version": "superbench.activation.v1",
  "fresh_payload": false,
  "finite_io": false,
  "receipt_lookup": false,
  "cancellation": false,
  "registration_observation": false
}
```

| Switch | Turns on |
| --- | --- |
| `fresh_payload` | Fresh payload layout for new and recovered workspaces, through the payload helper |
| `finite_io` | The finite-I/O profile on v2 `run` and receipt lookup ([finite I/O](finite-io.md)) |
| `receipt_lookup` | `POST /v2/receipts/lookup` ([receipt lookup](receipt-lookup.md)) |
| `cancellation` | v2 `cancel` and the operation reconciler ([cancellation](operation-cancellation.md)) |
| `registration_observation` | `POST /v2/registrations/observe` |

An omitted switch is off. An unknown, duplicated or non-boolean key, a missing or
different version, or trailing data refuses startup. `receipt_lookup` and
`cancellation` also move the socket to the private connection classes. Any of
`receipt_lookup`, `cancellation` and `registration_observation` makes each
connection carry one response (`Connection: close`); `registration_observation`
does this even on its own.

Some switches depend on others. benchd checks these at start, before it opens
the socket, and refuses a file that breaks one:

| Switch | Requires | Why |
| --- | --- | --- |
| `fresh_payload` | `cancellation` | Fresh materialization runs inside the operation scope that only the cancellation path claims. Without it every v2 `create` or `recover` would record a durable `materializing` workspace and then fail. |
| `fresh_payload` | the installed payload policy and service identity | benchd resolves `/etc/superbench/service-identity.json` (with `--identity`, above) for its own uid at start, for the same reason. |
| `finite_io` | `fresh_payload` | A finite run executes only on a Fresh payload tree. |
| `registration_observation` | `fresh_payload` | Observation reads only a Fresh payload tree. |
| `receipt_lookup`, `cancellation` | nothing | |

So the registered launch, which needs all five, sets every switch to `true`, and
`receipt_lookup` or `cancellation` may run alone.

When the file exists, benchd refuses to start unless it is a single-link regular
file of at most 4 KiB, owned by root and not group- or world-writable, and every
directory from `/` to `/etc/superbench` is owned by root without group or world
write. Symlinks are never followed. A missing file, or a missing
`/etc/superbench`, keeps every switch off whatever those directories look like,
and the daemon behaves exactly as without this feature. benchd runs as the
`superbench` account, so it must be able to search `/etc/superbench` (the
installer creates it with mode 0755). If it cannot, benchd cannot tell whether
the file exists and refuses to start. At start
benchd logs one line with the effective switches, for example
`activation: fresh_payload=false finite_io=false receipt_lookup=false cancellation=false registration_observation=false`.

An activated daemon never migrates its state. After installing or upgrading
benchd, start it once with every switch off so it brings the state current, stop
it, then edit the file and start it again. Otherwise benchd refuses with
`activated paths need state migrated by a start without them` before it opens the
socket. Restart the service to apply any change. Turning a switch off later stops
new work on that path; it does not erase obligations already recorded.

## 2. Configure the AgentPod Node

Use an enrolled Linux Node with the intended station and current control pair.
Install its matching `agentpod-bench-worker-linux-amd64` or
`agentpod-bench-worker-linux-arm64` artifact for the host architecture. Record
SHA-256 digests of the **installed bytes** of both worker and running Node.
The coordinator implementation digest refers to the Node executable, not benchd.

Add the following key to the Node's existing private JSON configuration, retaining
its enrollment and other settings:

```json
{"proposedCheckpointRegistry":"/absolute/private/coordinator-registry.json"}
```

On Linux, the default Node configuration is
`$XDG_CONFIG_HOME/agentpod-node/config.json`, or
`$HOME/.config/agentpod-node/config.json` when XDG_CONFIG_HOME is unset, for the
actual Node service account. This is a JSON setting, not an environment switch.
Empty or omitted disables this adapter. Restart the Node to apply configuration.

The registry is a strict JSON object with these fields; there are no extra keys:

| Field | Value |
| --- | --- |
| `version` | `agentpod.private-coordinator-registry.v1` |
| `node_id`, `host_id` | Actual enrolled Node and configured host identities |
| `socket` | `/run/superbench/benchd.sock` for the installation above |
| `data_root` | `/srv/superbench-data` for the installation above |
| `state_root` | Private Node coordinator journal directory, **not benchd's SQLite directory** |
| `intent_root` | Separate private Node adapter intent directory |
| `context_parent` | Separate private Node worker-context directory |
| `worker_path`, `worker_sha256` | Physical installed worker executable and its exact SHA-256 |
| `coordinator_implementation_sha256` | SHA-256 of the actual Node executable |
| `profiles` | One to sixteen explicit local profiles described below |

Use physical absolute paths, private directory mode 0700 and file mode 0600;
symlinks are refused. The three private Node directories must be distinct and
outside `data_root`. Do not reuse SuperBench state or expose these directories to
payloads. The registry is at most 64 KiB. Hash its exact final bytes only after
writing it; that digest is the Hub target's `registry_sha256`.

Each local profile contains `id`, `tenant_id`, `board_id`, `agent_id`,
`station_id`, `checkpoint_repository`, `repositories`, `arguments` and
`environment`. Use actual assigned IDs. Repositories are sorted by logical
`name`; each contains `name`, private clone identity `repository`, `base` and
`object_format` (`sha1` or `sha256`). The checkpoint repository must name a member.
`arguments` is the trusted harness command with an absolute executable as its
first entry; it is not the separate worker executable. Environment keys are
limited to `PATH`, `LANG`, `LC_ALL`, `TZ`, `TERM`. No credentials belong here.

A registry pin does not confer authority. The Node still verifies current
occupancy, managed host state, installed executables and the exact request.
Changing a binary or registry requires corresponding reviewed pins and restart;
do not substitute guessed digests to obtain a readiness response.

## 3. Configure the AgentPod Hub

Only after the accepted decision's enablement gates have been met, configure:

```ini
PROPOSED_CHECKPOINT_COORDINATOR=true
PROPOSED_CHECKPOINT_COORDINATOR_PROFILE_FILE=/absolute/private/checkpoint-profiles.json
```

Only literal `true` enables selection. The profile file and parent must be
physical, owned by root or the Hub service user, and inaccessible to others
(0700 parent, 0600 file). The file is at most 64 KiB and has this envelope:

```json
{"version":"agentpod.checkpoint-profiles.v1","profiles":[]}
```

The empty array is a shape illustration and is **not valid configuration**: supply
one to sixteen strict profiles. Each contains all of:

- `version`: `agentpod.checkpoint-profile.v1`;
- `organizationId`, `sourceTenantId`, `sourceOrigin`, `sourceAudience`;
- `reporterPrincipalId`, `ownerPrincipalId`, `occupantPrincipalId`;
- `agentKey`, `profileKey`, positive `occupancyEpoch`;
- `target`, `repositories`, `checkpointRepository`.

The source origin and audience are exact HTTPS origins without trailing slashes,
paths, queries or fragments. The source tenant is SuperPipeline's tenant;
`target.tenant_id` is AgentPod's local tenant. Map both to the same issuer
organization rather than assuming their identifier strings are interchangeable.
Owner, occupant, station and epoch must describe the actual current control pair.

`target` contains `tenant_id`, `board_id`, `agent_id`, `station_id`, `node_id`,
`host_id`, `registry_sha256`, `coordinator_implementation_sha256`,
`execution_mode` = `linux-cgroup-v2`, `bench_protocol` = `superbench.v2`,
`worker_version` = `agentpod.superbench-worker.v2`, and `worker_sha256`.
Match the Node registry and installed artifacts exactly.

Each Hub repository mapping has `name`, `repositorySHA256`, `provider` (`forge`
or `github`), `repository` (`owner/repo`) and `objectFormat`. Sort by name.
`repositorySHA256` hashes the exact private clone identity in the Node registry;
the public provider/repository allowlist must describe that same repository.
`checkpointRepository` must match the Node and stage policy. Never publish the
private clone identity as checkpoint evidence.

Use the Hub's existing organization-plane issuer configuration and Hub-only
service credential. The selected reporter must be the actual issuer service
principal, authorized for the exact organization and SuperPipeline audience,
with `checkpoints:submit` and `checkpoints:read` and the required product
entitlement. Provisioning remains an operator gate; a profile does not create a
grant. Never copy the reporter credential to the Node, worker or payload.

Restart the Hub after changing or repairing the profile file. It takes an
immutable per-path snapshot, including a failed load. A malformed profile disables
dedicated selection and logs a closed refusal code once; unrelated ordinary
agents continue. Checkpoint-required work must not fall back to generic ACP.
See AgentPod's [checkpoint reporting guide](https://github.com/SuperJackfruitLabs/agentpod/blob/main/docs/contributor/bench-checkpoint-reporting.md).

## 4. Opt in one SuperPipeline board stage

Keep the existing issuer/tenant mapping valid. In the isolated rehearsal
deployment, set all three switches to literal `true`:

```ini
PROPOSED_CHECKPOINT_POLICY=true
PROPOSED_CHECKPOINT_SOURCE=true
PROPOSED_CHECKPOINT_SETTLEMENT=true
```

The switches are independent; none provisions credentials or enables another.
An issuer-authenticated workspace owner/admin applies a policy to the intended
stage through `PATCH /v1/boards/:id/stages/:stageKey`. Agents cannot select their
own reporter. Do not change policy while the stage has active work.

Example request body, with placeholders replaced by actual enrolled identities:

```json
{
  "checkpointPolicy": {
    "version": "superpipeline.checkpoint-policy.v1",
    "kind": "superbench",
    "allowUnchanged": false,
    "repositories": [
      {"name":"app","provider":"forge","repository":"example/app"}
    ],
    "checkpointRepository": "app",
    "reporterPrincipalId": "ACTUAL_ISSUER_SERVICE_PRINCIPAL",
    "nodeId": "ACTUAL_NODE_ID",
    "executors": [{"agentId":"ACTUAL_AGENT_ID","stationId":"ACTUAL_STATION_ID"}]
  }
}
```

Use the complete sorted repository vector matching Hub and Node. A code-producing
stage uses `allowUnchanged:false`; permit unchanged output only through an
explicit verification-stage decision. Never create empty commits to simulate a
change. Leave ordinary stages policy-free; omit `checkpointPolicy` from ordinary
contexts rather than emitting `checkpointPolicy:null`.

The dedicated AgentPod adapter negotiates `superpipeline.checkpoint-claim.v1`
and checkpoint-only selection. It retains source admission before execution and
settles through the service-only checkpoint routes. Generic run completion does
not satisfy a policy-required run. The service endpoints are, under
`/v1/boards/:boardId/runs/:runId`, POST/GET `checkpoint-source`, POST
`checkpoint-settlements` and GET `checkpoint`. Do not manually forge their bodies
to get a card through the gate.

## 5. Rehearsal evidence and stop conditions

The rehearsal owner records the selected source revisions, installed binary and
registry digests, managed host status, current enrollment/control pair, effective
flags and exact stage policy before choosing a real card. The repeatable rehearsal
must observe create, lease, actual harness execution, stopped/fenced writers,
checkpoint with remote confirmation, release and authenticated settlement against
the same claim. Source admission, a checksum or a parked ledger row alone is not
that proof. Report each integration gap instead of weakening a refusal.

On uncertain launch, missing receipt, changed owner, failed checkpoint or lost
reply, retain the exact journals and leases for reconciliation. Do not repeat the
child, delete state, expire a lease by elapsed time, or reset work to force success.
On shutdown, systemd kills the service cgroup; inspect durable recovery before
assuming release. Disabling flags prevents new dedicated work but does not erase
existing obligations. No production tag or deployment is part of this guide.
