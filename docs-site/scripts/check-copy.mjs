// Run after `astro build`: everything the site publishes may say only what is true today. Adapted
// from landing/scripts/check-copy.mjs. SuperBench's latest release is v0.0.2, an experimental
// single-host release, so every HTML page must carry the release-status line
// (src/components/Banner.astro), naming the release these docs describe (DOCUMENTED in
// scripts/releases.mjs) and linking to its release page, plus a link to the repository. Nothing
// published (HTML, each page's .md route, llms.txt, llms-full.txt, llms-small.txt) may break an
// honesty rule in scripts/honesty.mjs: a version number must be a released tag, and install
// commands appear only on the install page. /llms-full.txt must exist and state the release and
// its limits: it is the file Captain Jack reads.
//
//   node scripts/check-copy.mjs          checks every .html, .md and .txt file under dist/
//   node scripts/check-copy.mjs <file>…  checks those files instead
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { mayInstall, problems as honesty } from './honesty.mjs';
import { DOCUMENTED, REPO, documented, releaseUrl } from './releases.mjs';

const dist = fileURLToPath(new URL('../dist/', import.meta.url));
// The release-status line, as Banner.astro words it.
const BANNER = [`SuperBench ${DOCUMENTED} is released`, 'experimental single-host release', 'not production software', 'trusted repositories'];
// What llms-full.txt must say about the release: its version and its limits.
const LLMS_FULL = [`SuperBench ${DOCUMENTED}`, 'experimental single-host release', 'Linux amd64', 'trusted repositories', 'cooperative', 'one host'];

const walk = (dir) => readdirSync(dir).flatMap((n) => {
  const p = join(dir, n);
  return statSync(p).isDirectory() ? walk(p) : /\.(html|md|txt)$/.test(n) ? [p] : [];
});
const files = process.argv.length > 2 ? process.argv.slice(2).map((f) => resolve(f)) : walk(dist);

const spaces = (s) => s.replace(/&nbsp;|&#160;|&#xa0;|\u00a0/gi, ' ');
const entities = (s) => s.replace(/&#39;|&#x27;|&apos;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>');

let failed = false;
const problem = (msg) => { failed = true; console.error(msg); };
if (!documented()) problem(`check-copy: ${DOCUMENTED} (scripts/releases.mjs) is not a release CHANGELOG.md has stamped`);

let html = 0;
for (const file of files) {
  const raw = readFileSync(file, 'utf8');
  const isHtml = /\.html$/.test(file);
  // For HTML, the words a reader is shown: no styles, no client scripts, no tags. Attribute values
  // (asset paths, the generator tag) are not copy.
  const text = isHtml
    ? entities(spaces(raw.replace(/<style[\s\S]*?<\/style>/gi, ' ').replace(/<script[\s\S]*?<\/script>/gi, ' ').replace(/<[^>]*>/g, ' '))).replace(/\s+/g, ' ')
    : raw;
  const fail = honesty(text, { install: mayInstall(relative(dist, file)) });
  if (isHtml) {
    html++;
    for (const phrase of BANNER) if (!text.includes(phrase)) fail.push(`missing the release-status line ("${phrase}")`);
    if (!raw.includes(`href="${releaseUrl(DOCUMENTED)}"`)) fail.push(`missing a link to ${releaseUrl(DOCUMENTED)}`);
    if (!raw.includes(`href="${REPO}"`)) fail.push(`missing a link to ${REPO}`);
  }
  if (fail.length) problem(`check-copy: ${relative(dist, file)}\n  ` + fail.join('\n  '));
}

const full = join(dist, 'llms-full.txt');
if (process.argv.length <= 2) {
  if (!existsSync(full)) problem('check-copy: dist/llms-full.txt is missing');
  else {
    const text = readFileSync(full, 'utf8').replace(/\s+/g, ' ');
    for (const phrase of LLMS_FULL) if (!text.includes(phrase)) problem(`check-copy: llms-full.txt does not say "${phrase}"`);
  }
  if (html < 22) problem(`check-copy: only ${html} HTML pages in dist/`);
}
if (failed) process.exit(1);
console.log(`check-copy: ok (${files.length} files, ${html} HTML pages carry the ${DOCUMENTED} release-status line)`);
