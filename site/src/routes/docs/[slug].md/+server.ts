import { error } from '@sveltejs/kit';
import { docBySlug, docMarkdown, docPages } from '#lib/docs.ts';
import type { EntryGenerator, RequestHandler } from './$types';

export const prerender = true;

export const entries: EntryGenerator = () => docPages.map((page) => ({ slug: page.slug }));

export const GET: RequestHandler = ({ params }) => {
  const page = docBySlug(params.slug);
  if (!page) error(404, 'No such page');
  return new Response(docMarkdown(page.slug), {
    headers: { 'content-type': 'text/markdown; charset=utf-8' }
  });
};
