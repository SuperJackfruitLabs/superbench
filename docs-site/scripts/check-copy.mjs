// Run after `astro build`: everything the site publishes may say only what is true today. Adapted
// from landing/scripts/check-copy.mjs. SuperBench is not released, so every HTML page must carry
// the pre-release banner (src/components/Banner.astro) and a link to the repository, and nothing
// published (HTML, each page's .md route, llms.txt, llms-full.txt, llms-small.txt) may break an
// honesty rule in scripts/honesty.mjs. /llms-full.txt must exist and say it is not released: it
// is the file Captain Jack reads.
//
//   node scripts/check-copy.mjs          checks every .html, .md and .txt file under dist/
//   node scripts/check-copy.mjs <file>…  checks those files instead
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { problems as honesty } from './honesty.mjs';

const REPO = 'https://github.com/SuperJackfruitLabs/superbench';
const BANNER = ['Not released yet.', 'experimental single-host prototype', 'Nothing is released or supported for use yet', 'refreshed at the first release'];
const dist = fileURLToPath(new URL('../dist/', import.meta.url));

const walk = (dir) => readdirSync(dir).flatMap((n) => {
  const p = join(dir, n);
  return statSync(p).isDirectory() ? walk(p) : /\.(html|md|txt)$/.test(n) ? [p] : [];
});
const files = process.argv.length > 2 ? process.argv.slice(2).map((f) => resolve(f)) : walk(dist);

const spaces = (s) => s.replace(/&nbsp;|&#160;|&#xa0;| /gi, ' ');
const entities = (s) => s.replace(/&#39;|&#x27;|&apos;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>');

let failed = false;
let html = 0;
for (const file of files) {
  const raw = readFileSync(file, 'utf8');
  const isHtml = /\.html$/.test(file);
  // For HTML, the words a reader is shown: no styles, no client scripts, no tags. Attribute values
  // (asset paths, the generator tag) are not copy.
  const text = isHtml
    ? entities(spaces(raw.replace(/<style[\s\S]*?<\/style>/gi, ' ').replace(/<script[\s\S]*?<\/script>/gi, ' ').replace(/<[^>]*>/g, ' '))).replace(/\s+/g, ' ')
    : raw;
  const fail = honesty(text);
  if (isHtml) {
    html++;
    for (const phrase of BANNER) if (!text.includes(phrase)) fail.push(`missing the pre-release banner ("${phrase}")`);
    if (!raw.includes(`href="${REPO}"`)) fail.push(`missing a link to ${REPO}`);
  }
  if (fail.length) { failed = true; console.error(`check-copy: ${relative(dist, file)}\n  ` + fail.join('\n  ')); }
}

const full = join(dist, 'llms-full.txt');
if (process.argv.length <= 2) {
  if (!existsSync(full)) { failed = true; console.error('check-copy: dist/llms-full.txt is missing'); }
  else if (!readFileSync(full, 'utf8').includes('not released yet')) { failed = true; console.error('check-copy: llms-full.txt does not say SuperBench is not released yet'); }
  if (html < 20) { failed = true; console.error(`check-copy: only ${html} HTML pages in dist/`); }
}
if (failed) process.exit(1);
console.log(`check-copy: ok (${files.length} files, ${html} HTML pages carry the pre-release banner)`);
