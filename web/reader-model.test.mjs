import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { parseObservation } from "./reader-contract.mjs";
import { inventoryModel, historyModel } from "./reader-model.mjs";
const read = (name) =>
  JSON.parse(
    readFileSync(new URL(`./contract/${name}`, import.meta.url), "utf8"),
  );
const corpus = read("observations.json"),
  pin = read("source.json");
for (const c of corpus.valid)
  test(`proposed reader accepts ${c.name}`, async () =>
    assert.deepEqual(
      await parseObservation(
        JSON.stringify(c.body),
        c.kind,
        c.request,
        c.status,
      ),
      c.body,
    ));
for (const c of corpus.invalid)
  test(`proposed reader refuses ${c.name}`, async () =>
    assert.rejects(
      parseObservation(JSON.stringify(c.body), c.kind, c.request, c.status),
    ));
for (const c of corpus.lexical)
  test(`proposed reader lexical refuses ${c.name}`, async () =>
    assert.rejects(parseObservation(c.raw, c.kind)));
test("consumer copies and portable codecs match exact proposed source pins", () => {
  assert.equal(pin.revision, "c169ffa94d40e86ecf369fc02d9964622854f388");
  for (const entry of Object.values(pin.files))
    assert.equal(
      createHash("sha256")
        .update(
          readFileSync(new URL(`./contract/${entry.file}`, import.meta.url)),
        )
        .digest("hex"),
      entry.sha256,
    );
  for (const [name, hash] of Object.entries({
    ...pin.codec_sources,
    ...pin.generated,
  }))
    assert.equal(
      createHash("sha256")
        .update(readFileSync(new URL(`./contract/${name}`, import.meta.url)))
        .digest("hex"),
      hash,
    );
  assert.match(pin.status, /PROPOSED/);
});
const named = (name) =>
  structuredClone(corpus.valid.find((c) => c.name === name));
test("inventory exposes observed host/size/checkpoint/holder; zero and unavailable stay distinct; no writing", async () => {
  const c = named("mixed_inventory");
  c.body.result.items[0].storage.used.bytes = 0;
  const model = await inventoryModel(JSON.stringify(c.body), c.request, 0);
  assert.equal(model.cards.length, 2);
  assert.equal(model.cards[0].host, "Development workspace");
  assert.equal(model.cards[0].used, "0 bytes");
  assert.equal(model.cards[0].allocated, "Unavailable");
  assert.equal(model.cards[0].checkpoint.members.length, 2);
  assert.match(model.cards[1].holder, /Current work run/);
  assert.equal(model.writable, false);
  assert.equal(model.cards[0].availability, "unleased");
  assert.equal(model.cards[0].token, undefined);
  assert.equal(model.cards[0].path, undefined);
  assert.equal(Object.hasOwn(model, "snapshot"), false);
  for (const card of model.cards) {
    assert.equal(Object.hasOwn(card, "benchId"), false);
    assert.equal(Object.hasOwn(card, "hostId"), false);
  }
  // The validated source keeps its full protocol identity; only projection drops it.
  const source = await parseObservation(JSON.stringify(c.body), "response", c.request, c.status);
  assert.equal(source.snapshot, c.body.snapshot);
  assert.equal(source.result.items[0].bench_id, c.body.result.items[0].bench_id);
  assert.equal(source.result.items[0].host.id, c.body.result.items[0].host.id);
});
test("transfer source remains blocked and stale observation never offers write authority", async () => {
  const c = named("transferred_source_blocked");
  const model = await inventoryModel(
    JSON.stringify(c.body),
    c.request,
    Date.parse(c.body.expires_at) + 1,
  );
  assert.equal(model.cards[0].availability, "blocked");
  assert.equal(model.cards[0].transferState, "transferred");
  assert.equal(model.stale, true);
  assert.equal(model.writable, false);
});
test("latest evidence is not relabeled a historical timeline or invented confirmation time", async () => {
  const c = named("checkpoints_response");
  const model = await historyModel(JSON.stringify(c.body), c.request, 0);
  assert.equal(model.coverage, "latest_only");
  assert.match(model.label, /earlier history unavailable/);
  assert.equal(model.entries[0].confirmedAt, undefined);
  const retained = named("retained_history");
  const history = await historyModel(
    JSON.stringify(retained.body),
    retained.request,
    0,
  );
  assert.equal(history.entries.length, 2);
  assert.equal(history.entries[0].confirmedAt, "2026-11-16T00:59:00Z");
});
test("raw response cannot smuggle private authority/path metadata into a display model", async () => {
  const c = named("mixed_inventory");
  c.body.result.items[0].token = "DO_NOT_DISCLOSE";
  await assert.rejects(inventoryModel(JSON.stringify(c.body), c.request));
});

test("unavailable inventory/history never invents missing pages or retained evidence", async () => {
  const failure = {
    version: "superbench.observation.v1",
    operation: "inventory",
    ok: false,
    error: { code: "unavailable", message: "Observation unavailable" },
  };
  const denied = await inventoryModel(
    JSON.stringify(failure),
    { version: failure.version, operation: "inventory" },
    0,
    503,
  );
  assert.equal(denied.error, "unavailable");
  assert.equal(denied.complete, true);
  assert.deepEqual(denied.cards, []);
  const read = named("mixed_inventory");
  read.body.result.items = [];
  const empty = await inventoryModel(
    JSON.stringify(read.body),
    read.request,
    0,
  );
  assert.equal(empty.cards.length, 0);
  assert.equal(empty.complete, true);
  const history = named("checkpoints_response");
  history.body.result.coverage = "unavailable";
  history.body.result.items = [];
  const unavailable = await historyModel(
    JSON.stringify(history.body),
    history.request,
    0,
  );
  assert.equal(unavailable.entries.length, 0);
  assert.match(unavailable.label, /history unavailable/);
});
