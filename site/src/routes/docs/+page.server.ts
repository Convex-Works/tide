import { docBySlug, docMarkdown } from '#lib/docs.ts';
import { render } from '#lib/server/render.ts';

export function load() {
  const page = docBySlug('index')!;
  return { page, ...render(docMarkdown(page.slug)) };
}
