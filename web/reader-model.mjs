import { parseObservation } from "./reader-contract.mjs";
const bytes = (value) =>
  value.status === "available"
    ? `${value.bytes.toLocaleString("en-US")} bytes`
    : "Unavailable";
function checkpoint(cp) {
  if (!cp) return { summary: "No confirmed checkpoint", members: [] };
  if (cp.kind === "scalar")
    return { id: cp.id, summary: cp.commit, members: [] };
  return {
    id: cp.id,
    summary: cp.seal,
    members: cp.members.map((member) => ({
      name: member.name,
      branch: member.branch,
      commit: member.commit,
      objectFormat: member.object_format,
    })),
  };
}
function timing(response, now) {
  return {
    observedAt: response.observed_at,
    expiresAt: response.expires_at,
    stale: now > Date.parse(response.expires_at),
  };
}
// Return display data, not raw operation records, authority or a transport configuration.
export async function inventoryModel(
  raw,
  request,
  now = Date.now(),
  status = 200,
) {
  const response = await parseObservation(raw, "response", request, status);
  if (!response.ok)
    return {
      error: response.error.code,
      cards: [],
      complete: true,
      writable: false,
    };
  if (response.operation !== "inventory") throw new Error("inventory_required");
  return {
    ...timing(response, now),
    complete: response.result.complete,
    writable: false,
    cards: response.result.items.map((item) => ({
      key: item.key,
      kind: item.kind,
      host: item.host.label,
      phase: item.phase,
      availability: item.availability,
      transferState: item.transfer_state,
      holder: item.holder
        ? `${item.holder.display} (${item.holder.source})`
        : "No current holder",
      used: bytes(item.storage.used),
      quota: bytes(item.storage.quota),
      allocated: bytes(item.storage.allocated),
      history: item.history,
      pendingCheckpoint: item.pending_checkpoint,
      checkpoint: checkpoint(item.checkpoint),
    })),
  };
}
export async function historyModel(
  raw,
  request,
  now = Date.now(),
  status = 200,
) {
  const response = await parseObservation(raw, "response", request, status);
  if (!response.ok)
    return {
      error: response.error.code,
      entries: [],
      complete: true,
      label: "Checkpoint history unavailable",
      writable: false,
    };
  if (response.operation !== "checkpoints")
    throw new Error("checkpoint_history_required");
  const r = response.result;
  return {
    ...timing(response, now),
    coverage: r.coverage,
    complete: r.complete,
    writable: false,
    label:
      r.coverage === "latest_only"
        ? "Latest confirmed checkpoint only — earlier history unavailable"
        : r.coverage === "unavailable"
          ? "Checkpoint history unavailable"
          : "Retained confirmed checkpoints — earlier evidence may be unavailable",
    entries: r.items.map((entry) => ({
      ...checkpoint(entry.checkpoint),
      confirmedAt:
        entry.time_source === "recorded" ? entry.confirmed_at : undefined,
      timeLabel:
        entry.time_source === "recorded"
          ? entry.confirmed_at
          : "Confirmation time unavailable",
    })),
  };
}
