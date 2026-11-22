// The releases the docs may name. A version is released when CHANGELOG.md, one directory up from
// docs-site/, has a stamped section for it (`## vX.Y.Z — YYYY-MM-DD`, written by the release-prep
// script before the tag) dated today or earlier. `## Unreleased`, an undated heading, a pre-release
// suffix or a future date does not count. The honesty rules refuse any other version number.
//
// DOCUMENTED is the release these docs describe: the banner names it, and check-copy requires it
// to be released. Change it when the docs are refreshed for a later release, not before.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export const DOCUMENTED = 'v0.0.1';
export const REPO = 'https://github.com/SuperJackfruitLabs/superbench';
export const releaseUrl = (tag) => `${REPO}/releases/tag/${tag}`;

const changelog = fileURLToPath(new URL('../../CHANGELOG.md', import.meta.url));

/** Released tags, newest first, as [{ tag: 'v0.0.1', date: '2026-11-22' }]. */
export function releases(text = readFileSync(changelog, 'utf8'), today = new Date().toISOString().slice(0, 10)) {
  return [...text.matchAll(/^## (v\d+\.\d+\.\d+) — (\d{4}-\d{2}-\d{2})\s*$/gm)]
    .map((m) => ({ tag: m[1], date: m[2] }))
    .filter((r) => r.date <= today);
}

/** The released versions with and without their "v", for matching copy ("v0.0.1", "0.0.1"). */
export function releasedVersions(list = releases()) {
  return new Set(list.flatMap((r) => [r.tag, r.tag.slice(1)]));
}

/** The release DOCUMENTED names, or null when it is not released. */
export const documented = (list = releases()) => list.find((r) => r.tag === DOCUMENTED) ?? null;
