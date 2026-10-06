import { configurationMarkdown } from './config.ts';

// The documentation, in reading order. One list drives the navigation, the
// pages, their Markdown twins and llms.txt, so they can't disagree.

export type DocPage = {
  slug: string;
  path: `/docs${string}`;
  title: string;
  summary: string;
  group: 'guide' | 'reference';
};

export const docPages: DocPage[] = [
  {
    slug: 'index',
    path: '/docs',
    title: 'Deploy',
    summary: 'Run tide on one server or on Kubernetes, with optional sign-in and recording.',
    group: 'guide'
  },
  {
    slug: 'configuration',
    path: '/docs/configuration',
    title: 'Configuration',
    summary: 'All environment variables.',
    group: 'reference'
  },
  {
    slug: 'local',
    path: '/docs/local',
    title: 'Local development',
    summary: 'Run tide on your computer.',
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
