// The honesty rules the docs share. Both checks use them: check-claims.mjs over the page sources
// before the build, check-copy.mjs over everything the build publishes (HTML, the .md routes and
// llms*.txt) after it.
//
// The rules follow the SuperBench section of the suite's claims ledger (SB-01 to SB-08) and the
// first release, v0.0.1, an experimental single-host release. A version number may name only a
// release CHANGELOG.md has stamped (scripts/releases.mjs); a claim that something "is released"
// must name one. Nothing may be worded as production-ready, multi-host or isolating untrusted
// code, and nothing may still say SuperBench is unreleased. Install commands belong on the install
// page alone (INSTALL_PAGES). Do not loosen a rule to let a page through.
import { releasedVersions } from './releases.mjs';

const RELEASED = releasedVersions();

/**
 * The pages, by path under dist/ or src/content/docs/, that may show install commands: the
 * install page and its .md route, and the llms files, which carry every page. Every other page is
 * still checked for them, as HTML and as its .md route.
 */
export const INSTALL_PAGES = ['start/install.md', 'start/install/index.html', 'llms-full.txt', 'llms-small.txt'];

/** Install commands and ways to get the software, allowed only on INSTALL_PAGES. */
export const INSTALL = [
  [/\bcurl\s+\S/i, 'a curl command'],
  [/\|\s*(?:ba|z)?sh\b/i, 'a pipe into a shell'],
  [/\bgo\s+(?:install|build|get|run)\b/i, 'a go install, build, get or run command'],
  [/\bapt(?:-get)?\s+(?:install|add)\b/i, 'an apt install'],
  [/\bbrew\s+(?:install|tap)\b/i, 'a brew install'],
  [/\bnpm\s+(?:i|install|add)\b/i, 'an npm install'],
  [/\bnpx\s+\S/i, 'an npx command'],
  [/\bdocker\s+(?:run|pull)\b/i, 'a docker run or pull'],
  [/\bpip3?\s+install\b/i, 'a pip install'],
  [/\binstall\.py\b/i, 'the installer command'],
  [/\bsystemctl\s+(?:enable|start)\b/i, 'a systemctl enable or start'],
];

/** Names of local hosts, agents and teams. The product word is "workspace". */
export const LOCAL_NAMES = [
  [/\b(?:workbench|guild|foundry|ashram|infra|codex|claude|hermes|sketchbook|estate)\b/i, 'a local host, agent or team name'],
  [/\bdeveloper experience\b/i, 'a local board or team name'],
];

/**
 * A version number. Only a released tag (with or without its "v") may appear: no unreleased,
 * future or pre-release version, and no Go or other tool versions either.
 */
export const VERSION = /\bv?\d+(?:\.\d+){2,}(?:-[0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)?/g;

/** What SuperBench said before its first release, and what is no longer true. */
export const STALE = [
  [/\bnot (?:yet )?(?:been )?released\b/i, 'a stale claim that SuperBench is not released'],
  [/\bnothing is released\b/i, 'a stale claim that nothing is released'],
  [/\bpre-?release (?:docs|banner)\b/i, 'the pre-release docs'],
  [/\bno (?:tags|published binaries)\b/i, 'a stale claim that there are no tags or binaries'],
  [/\b(?:refreshed|revised) at the first release\b/i, 'a stale promise about the first release'],
];

/** Overclaims, matched anywhere. */
export const OVERCLAIMS = [
  [/production[- ]ready/i, '"production-ready"'],
  [/secure isolation/i, '"secure isolation"'],
  [/\b(?:stable|supported) release\b/i, 'a claim of a release'],
  [/\b(?:multi-host|cross-host)\s+(?:coordination|service|support|release|leases?)\b/i, 'a multi-host claim'],
  [/\bgeneral(?:ly)? availab/i, 'a general-availability claim'],
  [/\b(?:enterprise|production)[- ]grade\b/i, 'a production-grade claim'],
  [/\bbattle[- ]tested\b/i, '"battle-tested"'],
];

/**
 * Words allowed only when negated: "not production", "not a sandbox", "nothing isolates untrusted
 * code", "no user or mount isolation", "per-bench isolation (none yet)".
 */
export const NEGATED_ONLY = [
  [/\bproduction\b/gi, 'the word "production"'],
  [/\bsandbox(?:ed|ing)?\b/gi, 'the word "sandbox"'],
  [/\bisolat\w*\s+untrusted\b/gi, 'isolating untrusted code'],
  [/\bisolat(?:ed|ion|es|ing|e)\b/gi, 'the words "isolated" or "isolation"'],
];

/** A release claim must name a released tag in the same sentence: "v0.0.1 is released". */
export const RELEASE_CLAIM = /\b(?:is|are|was|been|now|already)\s+released\b/gi;
const NEGATION = /\b(?:not|no|nor|never|nothing|none|isn't|aren't|without|neither)\b/i;

/** The ledger caveats that must travel with their claims: page, phrase, why. */
export const CAVEATS = [
  ['concepts/leases.md', 'cooperative', 'SB-01: leases are cooperative'],
  ['concepts/leases.md', 'no automatic expiry', 'SB-01: there is no automatic expiry'],
  ['concepts/leases.md', 'one host', 'SB-01: single host'],
  ['concepts/checkpoints.md', 'no test forces a mismatch', 'SB-02: the remote match is read from source'],
  ['concepts/quotas-and-stores.md', 'managed hosts only', 'SB-03: managed hosts only'],
  ['concepts/quotas-and-stores.md', 'recorded and not enforced', 'SB-03: elsewhere a request is not enforced'],
  ['concepts/quotas-and-stores.md', 'shares its limits', 'SB-04: the shared store shares the managed limits'],
  ['concepts/quotas-and-stores.md', 'grants no ordinary lease', 'SB-04: a failed refresh refuses a lease'],
  ['concepts/archive-and-reap.md', 'reviewed by a person', 'SB-05: archive and reap are reviewed administration'],
  ['concepts/archive-and-reap.md', 'off until an operator', 'SB-05: automatic retention is off until enabled'],
  ['concepts/archive-and-reap.md', 'only on managed Linux hosts', 'SB-05: quota retirement is managed-only'],
  ['concepts/supervised-runs.md', 'Linux only', 'SB-07: Linux only'],
  ['concepts/supervised-runs.md', 'not a sandbox', 'SB-07: fencing, not isolation'],
  ['concepts/supervised-runs.md', 'payload run under a separate user is not done', 'SB-07: no separate payload user'],
  ['start/security-model.md', 'no test asserts the absence of a TCP socket', 'SB-08: read from source only'],
  ['start/security-model.md', 'trusted repositories', 'the threat model: trusted repositories'],
  ['concepts/payload-identity.md', 'does not yet install and activate', 'the payload user is not active on main'],
  ['concepts/fresh-allocations.md', 'Fresh payload materialization remains disabled', 'Fresh materialization is disabled'],
  ['start/status.md', 'experimental single-host release', 'the status page says what v0.0.1 is'],
  ['start/status.md', 'not a supported installation', 'the status page says the installer is unsupported'],
  ['start/install.md', 'experimental single-host release', 'the install page says what v0.0.1 is'],
  ['start/install.md', 'Linux amd64 only', 'the release builds one platform'],
  ['start/install.md', 'sha256sum --check SHA256SUMS', 'the install page verifies the download'],
  ['start/install.md', 'disposable', 'install on a disposable or staging host'],
  ['start/install.md', 'trusted repositories', 'the threat model travels with the install'],
];

/** Is `name` (a path under dist/ or src/content/docs/) a page that may show install commands? */
export const mayInstall = (name) => INSTALL_PAGES.includes(name.replace(/\\/g, '/'));

/** The sentence around `index`: back to the last stop, on to the next. */
const sentence = (text, index) => {
  const start = Math.max(...['. ', '! ', '? ', '\n\n'].map((s) => text.lastIndexOf(s, index)), -1) + 1;
  const ends = ['. ', '! ', '? ', '\n\n'].map((s) => text.indexOf(s, index)).filter((i) => i >= 0);
  return text.slice(start, ends.length ? Math.min(...ends) : text.length);
};

/** Every problem in `text`, as "what: context". `install` allows install commands. */
export function problems(text, { install = false } = {}) {
  const found = [];
  const ctx = (m) => text.slice(Math.max(0, m.index - 40), m.index + m[0].length + 40).replace(/\s+/g, ' ');
  for (const [re, what] of [...(install ? [] : INSTALL), ...LOCAL_NAMES, ...STALE, ...OVERCLAIMS]) {
    const m = text.match(re);
    if (m) found.push(`${what}: "${ctx(m)}"`);
  }
  for (const m of text.matchAll(VERSION)) {
    if (!RELEASED.has(m[0])) found.push(`a version number that is not a released tag (${m[0]}): "${ctx(m)}"`);
  }
  for (const m of text.matchAll(RELEASE_CLAIM)) {
    const names = [...sentence(text, m.index).matchAll(VERSION)].some((v) => RELEASED.has(v[0]));
    if (!names) found.push(`a claim that it is released, naming no released tag: "${ctx(m)}"`);
  }
  for (const [re, what] of NEGATED_ONLY) {
    for (const m of text.matchAll(re)) {
      // The negation must come in the same sentence, within a few words before the match.
      const before = text.slice(Math.max(0, m.index - 60), m.index).split(/[.!?:;]\s/).pop();
      // Or a short parenthetical right after it, before the clause ends: "isolation (none yet)".
      const after = text.slice(m.index + m[0].length, m.index + m[0].length + 20).match(/^\s*\(([^)]*)\)/)?.[1] ?? '';
      if (!NEGATION.test(before) && !NEGATION.test(after)) found.push(`${what}, not negated: "${ctx(m)}"`);
    }
  }
  return found;
}
