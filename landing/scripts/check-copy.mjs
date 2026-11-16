// Run after `npm run build`: the page may say only what is true today. SuperBench is not released,
// so the page must say so and point at the source, and it must never offer an install, name a
// version, claim more than the claims ledger allows, or name a local host or agent.
//
//   node scripts/check-copy.mjs              checks dist/index.html
//   node scripts/check-copy.mjs <file.html>  checks that file instead
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const REPO = 'https://github.com/SuperJackfruitLabs/superbench';

const file = process.argv[2] ? resolve(process.argv[2]) : new URL('../dist/index.html', import.meta.url);
const html = readFileSync(file, 'utf8');

// What a reader or a crawler sees: the markup without styles or client scripts. Attribute values
// stay in (meta descriptions, alt text, labels), and so does the JSON-LD, which states the page.
const shown = html
  .replace(/<style[\s\S]*?<\/style>/gi, ' ')
  .replace(/<script(?![^>]*application\/ld\+json)[\s\S]*?<\/script>/gi, ' ');
// The words alone, for the phrases the page must carry.
const text = shown.replace(/<[^>]*>/g, ' ').replace(/&middot;|&#183;/g, '·').replace(/\s+/g, ' ');

const fail = [];
for (const phrase of ['Experimental', 'not released yet']) if (!text.includes(phrase)) fail.push(`missing "${phrase}"`);
if (!new RegExp(`href="${REPO.replace(/[.]/g, '\\.')}/?"`).test(html)) fail.push(`missing a link to ${REPO}`);

const forbidden = [
  [/production-ready/i, 'production-ready'],
  [/secure isolation/i, 'secure isolation'],
  [/npm install/i, 'npm install'],
  [/go install/i, 'go install'],
  [/curl /i, 'curl '],
  [/\bv\d+\.\d+\.\d+/i, 'a version number'],
  // Local names as words, so "infrastructure" is not a hit.
  [/\b(guild|workbench|foundry|ashram|infra)\b/i, 'a local name'],
];
for (const [re, what] of forbidden) {
  const m = shown.match(re);
  if (m) fail.push(`contains ${what}: "${shown.slice(Math.max(0, m.index - 30), m.index + m[0].length + 30).replace(/\s+/g, ' ')}"`);
}

if (fail.length) { console.error(`check-copy: ${file}\n` + fail.join('\n')); process.exit(1); }
console.log('check-copy: ok');
