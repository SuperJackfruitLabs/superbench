// Run after `astro build`: every internal link in dist/ resolves to a built file, and every #anchor
// to an id on its page. External links are not fetched. No dependencies: Node alone.
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = fileURLToPath(new URL('../dist/', import.meta.url));
const SITE = 'https://docs.superbench.dev';
const walk = (dir) => readdirSync(dir).flatMap((n) => {
  const p = join(dir, n);
  return statSync(p).isDirectory() ? walk(p) : n.endsWith('.html') ? [p] : [];
});

/** The built file a site path is served from, or null. */
function target(path) {
  const clean = decodeURIComponent(path.split(/[?#]/)[0]);
  for (const c of [clean, join(clean, 'index.html'), `${clean.replace(/\/$/, '')}.html`]) {
    const p = join(dist, c);
    if (existsSync(p) && statSync(p).isFile()) return p;
  }
  return null;
}
const ids = new Map();
const idsOf = (file) => {
  if (!ids.has(file)) ids.set(file, new Set([...readFileSync(file, 'utf8').matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])));
  return ids.get(file);
};

const broken = [];
let checked = 0;
const pages = walk(dist);
for (const file of pages) {
  const html = readFileSync(file, 'utf8').replace(/<template[\s\S]*?<\/template>/g, '');
  const here = '/' + relative(dist, file).replace(/index\.html$/, '').replace(/\.html$/, '');
  for (const m of html.matchAll(/\s(?:href|src)="([^"]+)"/g)) {
    let url = m[1].replace(/&amp;/g, '&');
    if (url.startsWith(SITE)) url = url.slice(SITE.length) || '/';
    if (/^(?:[a-z]+:|\/\/)/i.test(url)) continue;
    checked++;
    const [path, hash] = url.split('#');
    const abs = path === '' ? here : path.startsWith('/') ? path : new URL(path, `https://x${here}`).pathname;
    const file2 = target(abs);
    if (!file2) { broken.push(`${here} -> ${url}`); continue; }
    if (hash && file2.endsWith('.html') && !idsOf(file2).has(decodeURIComponent(hash))) broken.push(`${here} -> ${url} (no #${hash})`);
  }
}
if (broken.length) {
  console.error(`check-links: ${broken.length} broken internal link(s):\n  ` + [...new Set(broken)].join('\n  '));
  process.exit(1);
}
console.log(`check-links: ok (${checked} internal links across ${pages.length} pages)`);
