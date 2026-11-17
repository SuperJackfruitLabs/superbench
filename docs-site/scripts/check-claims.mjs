// The docs claim what the code on main does, and no more. Run before every build (`npm run build`),
// so a page that names a flag, an operation, an error or a setting the code does not have, or that
// breaks an honesty rule (scripts/honesty.mjs), fails the build.
//
// It reads the Go source one directory up from docs-site/:
//   cmd/bench/main.go, cmd/bench/workspace.go   every command and flag, and the flag defaults
//   cmd/benchd/main.go                         every daemon flag
//   internal/protocolv2/request.go             every v2 operation and its required/optional fields
//   internal/protocolv2/response_parse.go      every error code and the HTTP statuses it may carry
//   internal/setup/config.go, internal/cachepolicy   the .bench/config.toml tables and cache keys
// and checks the pages under src/content/docs against them. Every page needs a title and a
// description, and the ledger caveats must stay on their pages. No dependencies: Node alone.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { CAVEATS, problems as honesty } from './honesty.mjs';

const site = resolve(fileURLToPath(import.meta.url), '../..');
const repo = resolve(site, '..');
const docsDir = join(site, 'src/content/docs');
const read = (p) => readFileSync(join(repo, p), 'utf8');

const problems = [];
const fail = (msg) => problems.push(msg);

// ---- the pages --------------------------------------------------------------------------------

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    return statSync(p).isDirectory() ? walk(p) : /\.mdx?$/.test(name) ? [p] : [];
  });
}
const pages = new Map(walk(docsDir).map((p) => [relative(docsDir, p), readFileSync(p, 'utf8')]));
const page = (name) => {
  const text = pages.get(name);
  if (text === undefined) throw new Error(`check-claims: no page ${name}`);
  return text;
};
const has = (text, code) => text.includes('`' + code + '`');
/** The rows of a Markdown table whose first cell is a code span, as arrays of trimmed cells. */
const rows = (text) =>
  text.split('\n').filter((l) => /^\| `/.test(l)).map((l) => l.split('|').slice(1, -1).map((c) => c.trim()));
const codes = (cell) => [...cell.matchAll(/`([^`]+)`/g)].map((m) => m[1]);

for (const [name, text] of pages) {
  const front = text.match(/^---\n([\s\S]*?)\n---/)?.[1] ?? '';
  if (!/^title: \S/m.test(front)) fail(`${name}: no title`);
  if (!/^description: \S/m.test(front)) fail(`${name}: no description`);
  // The honesty rules, over the whole source (frontmatter, prose and code blocks). MDX imports
  // name package paths, not claims, so they are left out.
  const body = text.replace(/^import .*$/gm, '');
  for (const p of honesty(body)) fail(`${name}: ${p}`);
}
for (const [name, phrase, why] of CAVEATS) {
  if (!page(name).replace(/\*\*/g, '').replace(/\s+/g, ' ').toLowerCase().includes(phrase.toLowerCase())) fail(`${name} lost a caveat (${why}): "${phrase}"`);
}

// ---- Go flag sets -----------------------------------------------------------------------------

/** Evaluate a Go flag default: literals, a<<b, n*time.Unit. Returns a string as `-h` prints it, or null. */
function goDefault(kind, expr) {
  expr = expr.trim();
  if (kind === 'Bool') return expr;
  if (kind === 'String') return /^"[^"]*"$/.test(expr) ? expr.slice(1, -1) : null;
  if (kind === 'Int' || kind === 'Uint64') {
    const shift = expr.match(/^(\d+)\s*<<\s*(\d+)$/);
    if (shift) return String(BigInt(shift[1]) << BigInt(shift[2]));
    return /^\d+$/.test(expr) ? expr : null;
  }
  if (kind === 'Duration') {
    const m = expr.match(/^(?:(\d+)\s*\*\s*)?time\.(Hour|Minute|Second)$/);
    if (!m) return null;
    const secs = Number(m[1] ?? 1) * { Hour: 3600, Minute: 60, Second: 1 }[m[2]];
    const h = Math.floor(secs / 3600), mi = Math.floor((secs % 3600) / 60), s = secs % 60;
    return h ? `${h}h${mi}m${s}s` : mi ? `${mi}m${s}s` : `${s}s`;
  }
  return null;
}
/** Flags defined as `<set>.<Kind>("name", default, "help")` in a Go source file. */
function flagsOf(source, set) {
  const re = new RegExp(`\\b${set}\\.(Bool|String|Int|Uint64|Duration)\\("([a-z-]+)",\\s*(.*)$`, 'gm');
  return [...source.matchAll(re)].map((m) => {
    // The default is everything before the last argument, the quoted help text.
    const def = m[3].replace(/,\s*"(?:[^"\\]|\\.)*"\)\s*$/, '');
    return { kind: m[1], name: m[2], def: goDefault(m[1], def) };
  });
}

// bench: the scalar flag set. Every flag is on the page with the code's default.
const benchMain = read('cmd/bench/main.go');
const benchFlags = flagsOf(benchMain, 'f');
if (benchFlags.length < 30) fail(`read only ${benchFlags.length} flags from cmd/bench/main.go`);
const benchPage = page('reference/bench.md');
const [scalarSection, workspaceSection] = benchPage.split('## bench workspace');
const scalarRows = new Map(rows(scalarSection).filter((r) => r[0].startsWith('`--')).map((r) => [codes(r[0])[0].slice(2), r]));
for (const f of benchFlags) {
  const row = scalarRows.get(f.name);
  if (!row) { fail(`reference/bench.md has no row for --${f.name}`); continue; }
  if (f.def !== null && f.def !== '' && !codes(row[1]).includes(f.def)) fail(`reference/bench.md: --${f.name}'s default is ${f.def}, the page says ${row[1]}`);
}
for (const name of scalarRows.keys()) if (!benchFlags.some((f) => f.name === name)) fail(`reference/bench.md names --${name}, which cmd/bench/main.go does not define`);

// bench: every command in the usage line and the separately dispatched ones is named on the page.
const usage = benchMain.match(/usage: bench <([^>]+)>/)?.[1];
if (!usage) fail('cmd/bench/main.go no longer prints its usage line; update check-claims');
const dispatched = [...benchMain.matchAll(/args\[0\] == "([a-z-]+)"/g)].map((m) => m[1]);
const commands = new Set([...(usage ?? '').split('|'), ...dispatched]);
for (const c of commands) if (!has(benchPage, c)) fail(`reference/bench.md does not name the command ${c}`);
for (const [file, label] of [['cmd/bench/cross_host.go', 'handoff'], ['cmd/bench/mirror_clones.go', 'mirror-clones']]) {
  const sub = read(file).match(new RegExp(`usage: bench ${label} <([^>]+)>`))?.[1];
  if (!sub) { fail(`${file} no longer prints its usage line; update check-claims`); continue; }
  for (const c of sub.split('|')) if (!has(benchPage, c)) fail(`reference/bench.md does not name ${label} ${c}`);
}

// bench workspace: its operations and flags.
const workspaceGo = read('cmd/bench/workspace.go');
const wsOps = workspaceGo.match(/usage: bench workspace <([^>]+)>/)?.[1]?.split('|') ?? [];
if (wsOps.length < 10) fail(`read only ${wsOps.length} workspace operations from cmd/bench/workspace.go`);
for (const op of wsOps) if (!has(workspaceSection ?? '', op)) fail(`reference/bench.md does not name workspace ${op}`);
const wsSet = workspaceGo.match(/(\w+) := flag\.NewFlagSet\(/)?.[1];
const wsFlags = wsSet ? flagsOf(workspaceGo, wsSet) : [];
if (wsFlags.length < 20) fail(`read only ${wsFlags.length} flags from cmd/bench/workspace.go`);
const wsNamed = new Set(rows(workspaceSection ?? '').flatMap((r) => codes(r[0])).map((c) => c.replace(/^--/, '')));
for (const f of wsFlags) if (!wsNamed.has(f.name)) fail(`reference/bench.md's workspace table has no --${f.name}`);
for (const name of wsNamed) if (!wsFlags.some((f) => f.name === name)) fail(`reference/bench.md's workspace table names --${name}, which workspace.go does not define`);

// benchd: every flag, with its default where the source states one plainly.
const benchd = read('cmd/benchd/main.go');
const daemonFlags = flagsOf(benchd, 'flag');
if (daemonFlags.length < 8) fail(`read only ${daemonFlags.length} flags from cmd/benchd/main.go`);
const daemonRows = new Map(rows(page('reference/benchd.md')).filter((r) => r[0].startsWith('`--')).map((r) => [codes(r[0])[0].slice(2), r]));
for (const f of daemonFlags) {
  const row = daemonRows.get(f.name);
  if (!row) { fail(`reference/benchd.md has no row for --${f.name}`); continue; }
  if (f.def !== null && f.def !== '' && !codes(row[1]).includes(f.def)) fail(`reference/benchd.md: --${f.name}'s default is ${f.def}`);
}
for (const name of daemonRows.keys()) if (!daemonFlags.some((f) => f.name === name)) fail(`reference/benchd.md names --${name}, which cmd/benchd does not define`);

// ---- the v2 API -------------------------------------------------------------------------------

const request = read('internal/protocolv2/request.go');
const shapesBlock = request.match(/var shapes = map\[string\]shape\{([\s\S]*?)\n\}/)?.[1] ?? '';
const list = (s) => (s === 'nil' || !s ? [] : [...s.matchAll(/"([a-z_]+)"/g)].map((m) => m[1]));
const shapes = new Map(
  [...shapesBlock.matchAll(/"([a-z_]+)":\s*\{(?:\[\]string\{([^}]*)\}|nil)?(?:,\s*(?:\[\]string\{([^}]*)\}|nil))?\}/g)].map((m) => [
    m[1],
    { required: list(m[2]).sort(), optional: list(m[3]).sort() },
  ]),
);
if (shapes.size < 12) fail(`read only ${shapes.size} v2 operations from internal/protocolv2/request.go`);
const version = request.match(/const Version = "([^"]+)"/)?.[1];
const ops = page('api/operations.md');
if (!version || !has(ops, `"version": "${version}"`) && !ops.includes(`"version": "${version}"`)) fail(`api/operations.md does not show the version ${version}`);
const opRows = new Map(rows(ops).filter((r) => r.length === 4).map((r) => [codes(r[0])[0], r]));
for (const [op, s] of shapes) {
  const row = opRows.get(op);
  if (!row) { fail(`api/operations.md has no row for ${op}`); continue; }
  const req = codes(row[1]).sort(), opt = codes(row[2]).sort();
  if (JSON.stringify(req) !== JSON.stringify(s.required)) fail(`api/operations.md: ${op} requires ${s.required.join(', ') || 'nothing'}; the page says ${req.join(', ') || 'nothing'}`);
  if (JSON.stringify(opt) !== JSON.stringify(s.optional)) fail(`api/operations.md: ${op} accepts ${s.optional.join(', ') || 'nothing'}; the page says ${opt.join(', ') || 'nothing'}`);
}
for (const op of opRows.keys()) if (!shapes.has(op)) fail(`api/operations.md names the operation ${op}, which internal/protocolv2 does not define`);
const growth = request.match(/func Growth[\s\S]*?case ([^:]+):/)?.[1];
for (const op of list(growth)) if (!has(ops.split('are growth operations')[0].split('\n').slice(-2).join('\n'), op)) fail(`api/operations.md does not list ${op} as a growth operation`);

// Errors: the table is the code's code-to-status map, row for row.
const parse = read('internal/protocolv2/response_parse.go');
const errorMap = new Map();
for (const m of parse.matchAll(/case ((?:"[a-z_]+",?\s*)+):\s*return ([^\n]+)/g)) {
  const statuses = [...m[2].matchAll(/\b(\d{3})\b/g)].map((x) => x[1]).sort();
  for (const code of list(m[1])) errorMap.set(code, statuses);
}
const capacity = parse.match(/code == "([a-z_]+)" && status == (\d{3})/);
if (capacity) errorMap.set(capacity[1], [capacity[2]]);
if (errorMap.size < 8) fail(`read only ${errorMap.size} error codes from internal/protocolv2/response_parse.go`);
const errorRows = new Map(rows(page('api/errors.md')).map((r) => [codes(r[0])[0], r]));
for (const [code, statuses] of errorMap) {
  const row = errorRows.get(code);
  if (!row) { fail(`api/errors.md has no row for ${code}`); continue; }
  const shown = [...row[1].matchAll(/\b(\d{3})\b/g)].map((x) => x[1]).sort();
  if (JSON.stringify(shown) !== JSON.stringify(statuses)) fail(`api/errors.md: ${code} comes with ${statuses.join(', ')}; the page says ${shown.join(', ')}`);
}
for (const code of errorRows.keys()) if (!errorMap.has(code)) fail(`api/errors.md names ${code}, which the code does not return`);

// ---- .bench/config.toml -----------------------------------------------------------------------

const config = read('internal/setup/config.go');
const settings = page('reference/repository-settings.md');
for (const m of config.matchAll(/toml:"([a-z]+)"/g)) {
  const key = m[1];
  if (!settings.includes(`[${key}]`) && !has(settings, key)) fail(`reference/repository-settings.md does not name ${key}`);
}
const cacheGo = readdirSync(join(repo, 'internal/cachepolicy')).filter((f) => f.endsWith('.go') && !f.endsWith('_test.go')).map((f) => read(`internal/cachepolicy/${f}`)).join('\n');
const cacheKeys = [...cacheGo.matchAll(/toml:"([a-z]+)"/g)].map((m) => m[1]);
if (cacheKeys.length < 5) fail(`read only ${cacheKeys.length} cache keys from internal/cachepolicy`);
const cacheRows = rows(settings.split('## `[caches]`')[1]?.split('## ')[0] ?? '').map((r) => codes(r[0])[0]);
if (JSON.stringify([...cacheRows].sort()) !== JSON.stringify([...cacheKeys].sort())) fail(`reference/repository-settings.md's cache table is not the cache keys: page ${cacheRows.join(', ')}, code ${cacheKeys.join(', ')}`);

if (problems.length > 0) {
  console.error(`check-claims: ${problems.length} claim(s) the code or the honesty rules do not back:`);
  for (const p of problems) console.error(`  - ${p}`);
  process.exit(1);
}
console.log(
  `check-claims: ${pages.size} pages; ${benchFlags.length + wsFlags.length} bench flags, ${commands.size} commands, ` +
    `${daemonFlags.length} benchd flags, ${shapes.size} v2 operations, ${errorMap.size} error codes and ${cacheKeys.length} cache keys ` +
    `match the code; ${CAVEATS.length} ledger caveats in place; no install, version, local name or overclaim`,
);
