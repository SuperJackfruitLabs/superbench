// Run after `npm run build`: the site may say only what is true today. SuperBench is not released,
// so the home page must say so and point at the source, and nothing it publishes may offer an
// install, name a version, claim more than the claims ledger allows, or name a local host or agent.
//
//   node scripts/check-copy.mjs               checks dist/index.html, dist/404.html, dist/llms.txt
//   node scripts/check-copy.mjs <file>…       checks those files instead
//
// Every file gets the forbidden checks. HTML files other than 404.html must also carry the status
// ("Experimental", "not released yet") and a link to the repository. A .txt file is scanned whole.
//
// At the first release the suite frame renders "Releases · vX.Y.Z" on the page, and this check
// fails on purpose: that is when the page should be revised, not when the check should loosen.
import { readFileSync } from 'node:fs';
import { basename, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO = 'https://github.com/SuperJackfruitLabs/superbench';
const dist = (name) => fileURLToPath(new URL(`../dist/${name}`, import.meta.url));
const files = process.argv.length > 2 ? process.argv.slice(2).map((f) => resolve(f)) : ['index.html', '404.html', 'llms.txt'].map(dist);

// Matched against everything a reader or crawler is given: for HTML, the markup without styles or
// client scripts (attribute values and JSON-LD stay in).
const FORBIDDEN = [
  [/production-ready/i, 'production-ready'],
  [/secure isolation/i, 'secure isolation'],
  [/npm install/i, 'npm install'],
  [/go install/i, 'go install'],
  [/curl /i, 'curl '],
  [/\bv\d+\.\d+\.\d+/i, 'a version number'],
  // Local names as words, so "infrastructure" is not a hit.
  [/\b(guild|workbench|foundry|ashram|infra)\b/i, 'a local name'],
];
// Matched against the words alone (tags stripped), where a URL or an asset path cannot trip them.
const FORBIDDEN_IN_TEXT = [
  [/\bproduction\b/i, 'the word "production"'],
  [/\b\d+\.\d+\.\d+\b/, 'a bare version number'],
];

const context = (s, m) => s.slice(Math.max(0, m.index - 30), m.index + m[0].length + 30).replace(/\s+/g, ' ');
const spaces = (s) => s.replace(/&nbsp;|&#160;|&#xa0;|\u00a0/gi, ' ');

let failed = false;
for (const file of files) {
  const raw = readFileSync(file, 'utf8');
  const isHtml = /\.html?$/i.test(file);
  const shown = spaces(
    isHtml
      ? raw.replace(/<style[\s\S]*?<\/style>/gi, ' ').replace(/<script(?![^>]*application\/ld\+json)[\s\S]*?<\/script>/gi, ' ')
      : raw,
  );
  const text = (isHtml ? shown.replace(/<[^>]*>/g, ' ') : shown).replace(/&middot;|&#183;/g, '·').replace(/\s+/g, ' ');

  const fail = [];
  if (isHtml && basename(file) !== '404.html') {
    for (const phrase of ['Experimental', 'not released yet']) if (!text.includes(phrase)) fail.push(`missing "${phrase}"`);
    if (!new RegExp(`href="${REPO.replace(/[.]/g, '\\.')}/?"`).test(raw)) fail.push(`missing a link to ${REPO}`);
  }
  for (const [re, what] of FORBIDDEN) {
    const m = shown.match(re);
    if (m) fail.push(`contains ${what}: "${context(shown, m)}"`);
  }
  for (const [re, what] of FORBIDDEN_IN_TEXT) {
    const m = text.match(re);
    if (m) fail.push(`contains ${what}: "${context(text, m)}"`);
  }
  if (fail.length) { failed = true; console.error(`check-copy: ${file}\n  ` + fail.join('\n  ')); }
}
if (failed) process.exit(1);
console.log(`check-copy: ok (${files.map((f) => basename(f)).join(', ')})`);
