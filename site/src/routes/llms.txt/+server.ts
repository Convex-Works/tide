import { docPages, markdownPath } from '#lib/docs.ts';

export const prerender = true;

// The index agents start from (llmstxt.org): what tide is, then every page.
export function GET() {
  const list = (group: 'guide' | 'reference') =>
    docPages
      .filter((page) => page.group === group)
      .map((page) => `- [${page.title}](${markdownPath(page)}): ${page.summary}`)
      .join('\n');
  const body = `# tide

> A simple, lightweight video conference service that you host yourself. One program contains the web app and the media server.

To deploy tide, follow [Deploy](/docs/index.md) step by step. All pages are also in one file: [llms-full.txt](/llms-full.txt).

## Guide

${list('guide')}

## Reference

${list('reference')}
`;
  return new Response(body, { headers: { 'content-type': 'text/plain; charset=utf-8' } });
}
