// Build a standalone design artifact from the actual pinned proposed reader codec.
// No transport, service data, credentials or execution path is involved.
import { readFile, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { inventoryModel, historyModel } from '../web/reader-model.mjs';
const escape = (value) => String(value).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
const fact = (label, value) => `<dt>${escape(label)}</dt><dd>${escape(value)}</dd>`;
function card(value) {
  return `<article><h3>${escape(value.key)}</h3><span class="state">${escape(value.availability)}</span><dl>${fact('Logical host', value.host)}${fact('Current holder display', value.holder)}${fact('Used / quota / allocated', `${value.used} / ${value.quota} / ${value.allocated}`)}${fact('Initialization', value.phase)}${fact('Last confirmed checkpoint', value.checkpoint.summary)}</dl>${value.pendingCheckpoint ? '<p class="warning">Checkpoint publication pending. The previous confirmed checkpoint remains separate.</p>' : ''}${value.transferState ? `<p class="warning">Source transfer: ${escape(value.transferState)}. Writing is blocked.</p>` : ''}</article>`;
}
function history(model) {
  return `<p class="warning">${escape(model.label)}</p><ol>${model.entries.map((entry) => `<li><p>${escape(entry.timeLabel)}</p><code>${escape(entry.summary)}</code>${entry.members.map((m) => `<p>${escape(m.name)} · ${escape(m.branch)} · <code>${escape(m.commit)}</code></p>`).join('')}</li>`).join('')}</ol>`;
}
export async function renderMockup(corpus) {
  const fixture = (name) => {
    const value = corpus.valid.find((item) => item.name === name);
    if (!value) throw new Error(`missing mockup fixture: ${name}`);
    return value;
  };
  const inventory = fixture('mixed_inventory');
  const instant = Date.parse(inventory.body.observed_at);
  const view = await inventoryModel(JSON.stringify(inventory.body), inventory.request, instant);
  const transferred = fixture('transferred_source_blocked');
  const blocked = await inventoryModel(JSON.stringify(transferred.body), transferred.request, instant);
  const expired = await inventoryModel(JSON.stringify(inventory.body), inventory.request, Date.parse(inventory.body.expires_at) + 1);
  const histories = [];
  for (const name of ['checkpoints_response', 'retained_history']) {
    const f = fixture(name);
    histories.push(await historyModel(JSON.stringify(f.body), f.request, instant));
  }
  return `<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; connect-src 'none'; script-src 'none'; base-uri 'none'; form-action 'none'">
<title>SuperBench · Reader state mockups</title>
<style>
:root{color-scheme:light;--ink:#17302a;--muted:#51635c;--edge:#d4dfd8;--paper:#f5f7f3;font:15px/1.55 system-ui,sans-serif;background:var(--paper);color:var(--ink)}*{box-sizing:border-box}body{margin:0}header,main,footer{max-width:1180px;margin:auto;padding:24px}header{display:flex;justify-content:space-between;gap:16px;flex-wrap:wrap;border-bottom:1px solid var(--edge)}h1{font-size:36px;letter-spacing:-1px;line-height:1.15}h2{font-size:24px;margin-top:0}h3{margin:0 0 12px}section{margin:32px 0 48px}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,300px),1fr));gap:16px}article,.panel{background:white;border:1px solid var(--edge);border-radius:10px;padding:24px}.state{display:inline-block;border:1px solid var(--edge);border-radius:20px;padding:3px 12px;font-size:13px}dl{margin-bottom:0}dt{font-size:12px;color:var(--muted);margin-top:14px}dd{margin:3px 0;overflow-wrap:anywhere}.warning{background:#edf2eb;padding:12px 16px;border-radius:6px}.muted,footer{color:var(--muted)}code{font-size:12px;overflow-wrap:anywhere}li{padding:0 0 18px 8px}li p{margin:4px 0}.scope{display:flex;flex-wrap:wrap;gap:8px}.scope span{border:1px solid var(--edge);padding:8px 12px;border-radius:5px}@media(max-width:600px){header,main,footer{padding:20px}h1{font-size:29px}article,.panel{padding:18px}}
</style>
<header><strong>SuperBench</strong><span>Design preview · pinned fixtures only</span></header>
<main><h1>The workspace between sessions.</h1><p class="muted">Static review states, not a connected reader. No service calls or commands. All labels below are illustrative.</p>
<section aria-labelledby="inventory"><h2 id="inventory">Inventory</h2><p>Observation ${escape(view.observedAt)}. Values are recorded observations, not current liveness.</p><div class="grid">${view.cards.map(card).join('')}</div></section>
<section aria-labelledby="transfer"><h2 id="transfer">Transferred source</h2><p>A ready initialization phase does not override a transfer block.</p><div class="grid">${blocked.cards.map(card).join('')}</div></section>
<section aria-labelledby="history"><h2 id="history">Checkpoint evidence</h2><p>A checkpoint records remote-confirmed committed work. It does not describe uncommitted files or prove a writer has stopped.</p><div class="grid">${histories.map((m) => `<article>${history(m)}</article>`).join('')}</div></section>
<section aria-labelledby="expired"><h2 id="expired">Expired observation</h2><div class="panel"><p class="warning">${expired.stale ? 'Expired fixture snapshot — refresh and reauthorization required.' : 'Fixture snapshot'}</p><p>Expires ${escape(expired.expiresAt)}. This design does not reconnect or reuse expired content authority.</p></div></section>
<section aria-labelledby="limits"><h2 id="limits">Content and control remain separate</h2><div class="scope"><span>Files: unavailable</span><span>Saved diffs: unavailable</span><span>Captured logs: unavailable</span><span>Terminal attachment: unavailable</span><span>Takeover: unavailable</span></div><p class="muted">Inventory access is not content access. Unleased is not permission to write. Unknown usage is not zero. EOF and snapshot hashes are not fencing evidence.</p></section></main>
<footer>Fixture-backed mockup only. No listener, credential input, live filesystem access or writer action. Reader contracts remain proposed; rendering does not establish producer safety or complete Phase 1 or Phase 2.</footer></html>\n`;
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const corpus = JSON.parse(await readFile(new URL('../web/contract/observations.json', import.meta.url), 'utf8'));
  await writeFile(new URL('../docs/mockups/reader-states.html', import.meta.url), await renderMockup(corpus));
}
