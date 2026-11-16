# Fresh Linux installation and checkpoint pilot configuration

This is the installation and configuration handoff for a supervised staging
rehearsal. The coordinator-grant design is accepted, subject to its gates before
enablement. Acceptance does not provision a service principal or prove a working
pilot. SuperBench remains standalone; the suite calls it, and SuperBench does not
call suite products. All suite checkpoint switches remain off by default.

**Current boundary:** main installs the trusted service-account execution model.
It does not yet install and activate the complete separate payload-UID path.
The payload helper, fresh registered layout bootstrap, RUN/setup/Git routing and
AgentPod's corresponding worker transport must land together before claiming that
boundary is enforced. Do not make Node-private worker contexts readable to the
payload account to work around an adapter refusal. Installation success and matching
hashes are not evidence of a successful card or writer fencing.

## Current gaps

AgentPod v0.1.129 cannot complete a card through the separate payload-identity
path. The following work must land and be verified before the rehearsal can
establish an end-to-end card result:

- Wire the coordinator to launch the registered worker with its exact retained
  input and consume its separate, binding-validated result after fencing and drain.
- Add the Hermes worker-protocol adapter, including prompt/result conversion and
  explicit provider configuration in the isolated payload home.
- Land the SuperBench #47–#54 payload-identity stack and complete its fresh-layout
  bootstrap, restricted RUN/setup/Git, installer and sudo integration. Merging
  those foundations alone does not activate every required route.
- Publish an operator-approved AgentPod release containing the coordinator wiring,
  adapter and compatible worker. Re-pin the installed Node and worker binaries and
  registry after that upgrade; v0.1.129 does not contain these changes.

The empty bridge-profile fix is merged in AgentPod #900, but it does not close
these gaps. Keep checkpoint flags off until the enablement gates and actual
lifecycle checks pass. Configuration, matching hashes and a successful install
are not evidence that a card can complete.

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
python3 deploy/linux/install.py --bin-dir /absolute/build/bin \
  --root /absolute/private/staging --managed
```

These are the three binaries required by the current main installer. Review the
staged files, owners, modes, systemd unit, wrapper and sudo rule before applying
anything. Staging writes only below the supplied physical absolute root. On a
fresh host, direct Linux root then runs:

```sh
python3 deploy/linux/install.py --bin-dir /absolute/build/bin --root / \
  --managed --prepare
```

This prepares the stable `superbench` account and directories and reloads systemd;
it does not start or enable the service. Existing conflicting files, unsafe paths
and an active installation refuse. This is not an upgrade or legacy-adoption
procedure. See [installer details](install.md) for preflight and recovery.

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
