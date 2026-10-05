import { docMarkdown, docPages } from '#lib/docs.ts';

export const prerender = true;

// Every docs page in reading order, as one Markdown file for agents.
export function GET() {
  const body = docPages.map((page) => docMarkdown(page.slug).trim()).join('\n\n---\n\n') + '\n';
  return new Response(body, { headers: { 'content-type': 'text/plain; charset=utf-8' } });
}
