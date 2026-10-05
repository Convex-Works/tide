import { error } from '@sveltejs/kit';
import { docBySlug, docMarkdown, docPages } from '#lib/docs.ts';
import { render } from '#lib/server/render.ts';
import type { EntryGenerator, PageServerLoad } from './$types';

export const entries: EntryGenerator = () =>
  docPages.filter((page) => page.slug !== 'index').map((page) => ({ slug: page.slug }));

export const load: PageServerLoad = ({ params }) => {
  const page = docBySlug(params.slug);
  if (!page || page.slug === 'index') error(404, 'No such page');
  return { page, ...render(docMarkdown(page.slug)) };
};
