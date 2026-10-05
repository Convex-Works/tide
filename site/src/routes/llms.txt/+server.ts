import { docPages, markdownPath } from '#lib/docs.ts';

export const prerender = true;

// The index agents start from (llmstxt.org): what tide is, then every page.
export function GET() {
  const list = (group: 'runbook' | 'reference') =>
    docPages
      .filter((page) => page.group === group)
      .map(
        (page) =>
          `- [${page.title}${page.step ? ` (step ${Number(page.step)})` : ''}](${markdownPath(page)}): ${page.summary}`
      )
      .join('\n');
  const body = `# tide

> Simple, self-hostable video conference service: one Go binary with its web app embedded, a media server, a recorder and redis, on Kubernetes.

To deploy tide, follow the runbook steps in order. Each ends with checks that prove it is done. The whole runbook is also one file: [llms-full.txt](/llms-full.txt).

## Runbook

${list('runbook')}

## Reference

${list('reference')}
`;
  return new Response(body, { headers: { 'content-type': 'text/plain; charset=utf-8' } });
}
