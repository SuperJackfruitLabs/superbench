// Run after `npm run build`: the site may say only what is true today. SuperBench's first release is
// v0.0.1, an experimental single-host release, so the home page must say so, link to that release
// and to the source, and nothing it publishes may offer a curl, npm or go install, name a version
// that is not a released tag (scripts/releases.mjs: stamped and dated in CHANGELOG.md), claim more
// than the claims ledger allows, still say it is unreleased, or name a local host or agent.
//
//   node scripts/check-copy.mjs               checks dist/index.html, dist/404.html, dist/llms.txt
//   node scripts/check-copy.mjs <file>…       checks those files instead
//
// Every file gets the forbidden checks. HTML files other than 404.html must also carry the status
// ("Experimental", the release DOCUMENTED names, "single-host release"), a link to that release
// and a link to the repository. llms.txt must name the release and say what it is. A .txt file is
// scanned whole.
//
// The suite frame renders "Releases · vX.Y.Z" for the latest release. Once a later release is
// stamped in CHANGELOG.md that version is allowed too; the page still names DOCUMENTED until it
// is refreshed.
import { readFileSync } from 'node:fs';
import { basename, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { DOCUMENTED, REPO, documented, releaseUrl, releasedVersions } from './releases.mjs';

const RELEASED = releasedVersions();
// Any three-part version, with a pre-release suffix if it has one; only a released tag may appear.
// In the markup only a "v" version counts (SVG path data is full of bare dotted numbers); in the
// words alone a bare one counts too.
const SUFFIX = '(?:-[0-9A-Za-z]+(?:\\.[0-9A-Za-z]+)*)?';
const VERSION_IN_MARKUP = new RegExp(`\\bv\\d+(?:\\.\\d+){2,}${SUFFIX}`, 'gi');
const VERSION_IN_TEXT = new RegExp(`\\bv?\\d+(?:\\.\\d+){2,}${SUFFIX}`, 'g');
const STATUS = ['Experimental', DOCUMENTED, 'single-host release'];
const LLMS = [`SuperBench ${DOCUMENTED}`, 'experimental single-host release', 'Linux amd64', 'trusted repositories', 'not a supported installation'];
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
  [/\b(?:multi|cross)-host\s+(?:coordination|service|support|release|leases?)\b/i, 'a multi-host claim'],
  [/\b(?:enterprise|production)[- ]grade\b/i, 'a production-grade claim'],
  [/\bgeneral(?:ly)? availab/i, 'a general-availability claim'],
  // Local names as words, so "infrastructure" is not a hit.
  [/\b(guild|workbench|foundry|ashram|infra)\b/i, 'a local name'],
];
// Matched against the words alone (tags stripped), where a URL or an asset path cannot trip them.
const FORBIDDEN_IN_TEXT = [
  [/\bproduction\b/i, 'the word "production"'],
  // What the page said before the first release.
  [/\bnot (?:yet )?released\b/i, 'a stale claim that it is not released'],
  [/\bpre-?release docs\b/i, 'the pre-release docs'],
  [/\bno (?:release|tags|binaries)\b/i, 'a stale claim that there is no release'],
  [/\bnothing to install\b/i, 'a stale claim that there is nothing to install'],
  [/\bfirst release is out\b/i, 'a stale promise about the first release'],
];

const context = (s, m) => s.slice(Math.max(0, m.index - 30), m.index + m[0].length + 30).replace(/\s+/g, ' ');
const spaces = (s) => s.replace(/&nbsp;|&#160;|&#xa0;|\u00a0/gi, ' ');

let failed = false;
if (!documented()) { failed = true; console.error(`check-copy: ${DOCUMENTED} (scripts/releases.mjs) is not a release CHANGELOG.md has stamped`); }
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
    for (const phrase of STATUS) if (!text.includes(phrase)) fail.push(`missing "${phrase}"`);
    if (!raw.includes(`href="${releaseUrl(DOCUMENTED)}"`)) fail.push(`missing a link to ${releaseUrl(DOCUMENTED)}`);
    if (!new RegExp(`href="${REPO.replace(/[.]/g, '\\.')}/?"`).test(raw)) fail.push(`missing a link to ${REPO}`);
  }
  if (basename(file) === 'llms.txt') for (const phrase of LLMS) if (!text.includes(phrase)) fail.push(`missing "${phrase}"`);
  // Versions in the markup (attributes and JSON-LD included) and in the words alone.
  for (const [s, re] of [[shown, VERSION_IN_MARKUP], [text, VERSION_IN_TEXT]]) {
    for (const m of s.matchAll(re)) if (!RELEASED.has(m[0])) fail.push(`contains a version number that is not a released tag (${m[0]}): "${context(s, m)}"`);
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
