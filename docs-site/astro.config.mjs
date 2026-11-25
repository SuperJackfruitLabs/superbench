// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import sjlDocs from '@superjackfruitlabs/vibekit-docs';

export default defineConfig({
  site: 'https://docs.superbench.dev',
  integrations: [
    starlight({
      title: 'SuperBench',
      description:
        'Docs for SuperBench v0.0.2, an experimental single-host release: one task, one git worktree, leased to one holder and checkpointed to the remote.',
      // The suite's docs theme: vibekit's cyan, the faces, the street, status badges, the footers,
      // the 404, the edit link and llms.txt. See @superjackfruitlabs/vibekit-docs's README.
      // SuperBench is still `docs: null` in vibekit-frame, so the street, switchers and footer
      // link to superbench.dev until frontdoor lists this site; the theme handles that itself.
      plugins: [
        sjlDocs({
          product: 'superbench',
          status: 'wet paint',
          // /llms.txt, /llms-full.txt and /llms-small.txt, for agents reading these docs.
          llms: {
            description:
              'SuperBench v0.0.2, released on 2026-11-23, is the second release, after v0.0.1: an experimental single-host ' +
              'release, not production software. It ships one archive for Linux amd64 (bench, benchd, ' +
              'bench-quota and bench-payload) with SHA256SUMS and a build-provenance attestation on the ' +
              'GitHub release, and these docs describe it. Over v0.0.1, a checkpoint or release refused ' +
              'for a dirty tree now names up to five of the paths to clean, and the docs explain workspaces ' +
              'whose repositories a card chooses upstream. A bench is one task\'s git worktree on ' +
              'one host, kept warm between holders: `bench create` makes it, `bench lease` hands it to one ' +
              'holder at a time, `bench checkpoint` pushes the existing HEAD without force and confirms the ' +
              'remote has exactly that commit, and `bench release` frees it. Leases are cooperative: they ' +
              'stop other bench acquisitions, not direct edits or other processes running as the same user, ' +
              'and they never expire on their own. SuperBench is for trusted repositories on one host; ' +
              'nothing in it isolates untrusted code.',
          },
        }),
      ],
      // Every page carries the release-status line: see src/components/Banner.astro.
      components: { Banner: './src/components/Banner.astro' },
      favicon: '/favicon.svg',
      head: [
        // The link preview, copied from landing/public/og.png so this host serves its own.
        // Absolute URLs: several unfurlers ignore a relative og:image.
        { tag: 'meta', attrs: { property: 'og:image', content: 'https://docs.superbench.dev/og.png' } },
        { tag: 'meta', attrs: { property: 'og:image:width', content: '1200' } },
        { tag: 'meta', attrs: { property: 'og:image:height', content: '630' } },
        { tag: 'meta', attrs: { property: 'og:image:alt', content: 'SuperBench — one task, one bench' } },
        { tag: 'meta', attrs: { name: 'twitter:image', content: 'https://docs.superbench.dev/og.png' } },
      ],
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/SuperJackfruitLabs/superbench' }],
      // The suite's groups, in the order a reader needs them. The concepts are "Use it"; the
      // security model and limits sit under Start, because they decide whether to read on.
      sidebar: [
        {
          label: 'Start',
          items: [
            { label: 'What SuperBench is', slug: 'start/what-it-is' },
            { label: 'Status', slug: 'start/status' },
            { label: 'Install v0.0.2', slug: 'start/install' },
            { label: 'Security model', slug: 'start/security-model' },
            { label: 'Limits', slug: 'start/limits' },
          ],
        },
        {
          label: 'Use it',
          items: [
            { label: 'Benches and workspaces', slug: 'concepts/benches-and-workspaces' },
            { label: 'Leases', slug: 'concepts/leases' },
            { label: 'Checkpoints', slug: 'concepts/checkpoints' },
            { label: 'Supervised runs and setup', slug: 'concepts/supervised-runs' },
            { label: 'Quotas, mirrors and stores', slug: 'concepts/quotas-and-stores' },
            { label: 'Archive and reap', slug: 'concepts/archive-and-reap' },
            { label: 'Payload identity and the fixed helpers', slug: 'concepts/payload-identity' },
            { label: 'Remote access', slug: 'concepts/remote-access' },
            { label: 'Fresh allocations and recovery', slug: 'concepts/fresh-allocations' },
          ],
        },
        {
          label: 'Build on it',
          items: [
            { label: 'The local API', slug: 'api/overview' },
            { label: 'v2 operations', slug: 'api/operations' },
            { label: 'Errors', slug: 'api/errors' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'bench CLI', slug: 'reference/bench' },
            { label: 'benchd', slug: 'reference/benchd' },
            { label: 'Repository settings', slug: 'reference/repository-settings' },
            { label: 'Files and paths', slug: 'reference/files-and-paths' },
          ],
        },
      ],
    }),
  ],
});
