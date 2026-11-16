import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { renderMockup } from '../scripts/render-reader-mockup.mjs';
const corpus = JSON.parse(await readFile(new URL('./contract/observations.json', import.meta.url), 'utf8'));
test('standalone mockup stays reproducible from the pinned real reader models', async () => {
  const rendered = await renderMockup(corpus);
  assert.equal(await readFile(new URL('../docs/mockups/reader-states.html', import.meta.url), 'utf8'), rendered);
  assert.match(rendered, /Latest confirmed checkpoint only/);
  assert.match(rendered, /Retained confirmed checkpoints/);
  assert.match(rendered, /Confirmation time unavailable/);
  assert.match(rendered, /Source transfer: transferred\. Writing is blocked/);
  assert.match(rendered, /Expired fixture snapshot/);
  assert.match(rendered, /Unavailable/);
  assert.doesNotMatch(rendered, /<script|<iframe|<form|<input|<button|\b(?:src|href)\s*=/i);
  assert.match(rendered, /connect-src 'none'; script-src 'none'/);
});
test('fixture task labels are escaped and never become links or executable markup', async () => {
  const changed = structuredClone(corpus);
  const f = changed.valid.find((x) => x.name === 'mixed_inventory');
  f.body.result.items[0].host.label = '<img src=x onerror="alert(1)">';
  const rendered = await renderMockup(changed);
  assert.match(rendered, /&lt;img src=x onerror=&quot;alert\(1\)&quot;&gt;/);
  assert.doesNotMatch(rendered, /<img|<script|<iframe|<form|\b(?:src|href)\s*=["']/i);
});
test('missing corpus states fail instead of inventing a successful empty screen', async () => {
  const changed = structuredClone(corpus);
  changed.valid = changed.valid.filter((x) => x.name !== 'transferred_source_blocked');
  await assert.rejects(renderMockup(changed), /missing mockup fixture: transferred_source_blocked/);
});

test('available zero remains distinct from unavailable in the static view', async () => {
  const changed = structuredClone(corpus);
  const f = changed.valid.find((x) => x.name === 'mixed_inventory');
  f.body.result.items[0].storage.used = { status: 'available', bytes: 0 };
  const rendered = await renderMockup(changed);
  assert.match(rendered, /0 bytes \/ 33,554,432 bytes \/ Unavailable/);
});
