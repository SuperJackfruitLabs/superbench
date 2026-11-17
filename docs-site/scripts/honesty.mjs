// The honesty rules the docs share: what no page may say while SuperBench is not released. Both
// checks use them: check-claims.mjs over the page sources before the build, check-copy.mjs over
// everything the build publishes (HTML, the .md routes and llms*.txt) after it.
//
// The rules follow the SuperBench section of the suite's claims ledger (SB-01 to SB-08): nothing
// may be worded as released, production-ready or isolating untrusted code, and no page offers an
// install. At the first release these rules are revised with the docs; do not loosen one to let a
// page through.

/** Install commands and ways to get the software. "There is no supported installation" is fine. */
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

/** A version number presented as a release (Go versions and the like are not named at all). */
export const VERSIONS = [[/\bv?\d+\.\d+\.\d+\b/, 'a version number']];

/** Overclaims, matched anywhere. */
export const OVERCLAIMS = [
  [/production[- ]ready/i, '"production-ready"'],
  [/secure isolation/i, '"secure isolation"'],
  [/\b(?:stable|supported) release\b/i, 'a claim of a release'],
  [/\b(?:multi-host|cross-host)\s+(?:coordination|service|leases?)\b/i, 'a multi-host claim'],
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
  [/\b(?:is|are|was|been|now|already)\s+released\b/gi, 'a claim that it is released'],
];
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
  ['start/status.md', 'not released', 'the status page says it is not released'],
];

/** Every problem in `text`, as "what: context". */
export function problems(text) {
  const found = [];
  const ctx = (m) => text.slice(Math.max(0, m.index - 40), m.index + m[0].length + 40).replace(/\s+/g, ' ');
  for (const [re, what] of [...INSTALL, ...LOCAL_NAMES, ...VERSIONS, ...OVERCLAIMS]) {
    const m = text.match(re);
    if (m) found.push(`${what}: "${ctx(m)}"`);
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
