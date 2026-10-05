import { configurationMarkdown } from './config.ts';

// The documentation, in reading order. One list drives the navigation, the
// pages, their Markdown twins and llms.txt, so they can't disagree.

export type DocPage = {
  slug: string;
  path: `/docs${string}`;
  title: string;
  /** The runbook step number, for the four steps that must happen in order. */
  step?: string;
  summary: string;
  group: 'runbook' | 'reference';
};

export const docPages: DocPage[] = [
  {
    slug: 'index',
    path: '/docs',
    title: 'Overview',
    summary: 'What runs, what you bring, and the order of the steps.',
    group: 'runbook'
  },
  {
    slug: 'prepare',
    path: '/docs/prepare',
    title: 'Prepare',
    step: '01',
    summary: 'Hostnames, OIDC client, bucket, media network path and secrets.',
    group: 'runbook'
  },
  {
    slug: 'install',
    path: '/docs/install',
    title: 'Install',
    step: '02',
    summary: 'Build the image, create secrets, write the Kubernetes overlay, apply.',
    group: 'runbook'
  },
  {
    slug: 'verify',
    path: '/docs/verify',
    title: 'Verify',
    step: '03',
    summary: 'Prove sign-in, calls, recording and downloads work.',
    group: 'runbook'
  },
  {
    slug: 'operate',
    path: '/docs/operate',
    title: 'Operate',
    step: '04',
    summary: 'Backups, upgrades, rollback and incident evidence.',
    group: 'runbook'
  },
  {
    slug: 'configuration',
    path: '/docs/configuration',
    title: 'Configuration',
    summary: 'Every environment variable, its default and its rule.',
    group: 'reference'
  },
  {
    slug: 'transcripts',
    path: '/docs/transcripts',
    title: 'Transcripts',
    summary: 'Optional transcripts made on hosts’ own computers.',
    group: 'reference'
  },
  {
    slug: 'local',
    path: '/docs/local',
    title: 'Local development',
    summary: 'Run tide on one machine.',
    group: 'reference'
  }
];

const sources = import.meta.glob<string>('../content/docs/*.md', {
  query: '?raw',
  import: 'default',
  eager: true
});

export function docBySlug(slug: string): DocPage | undefined {
  return docPages.find((page) => page.slug === slug);
}

/** The page's Markdown, as served at its .md path and in llms-full.txt. */
export function docMarkdown(slug: string): string {
  if (slug === 'configuration') return configurationMarkdown();
  const source = sources[`../content/docs/${slug}.md`];
  if (source === undefined) throw new Error(`no Markdown for docs page ${slug}`);
  return source;
}

export function markdownPath(page: DocPage): string {
  return page.slug === 'index' ? '/docs/index.md' : `${page.path}.md`;
}
