import { docPages } from '#lib/docs.ts';
import { siteURL } from '#lib/links.ts';

export const prerender = true;

// Every page of the site for search engines: the home page, then the docs
// from the list that builds them, so a new page is listed when it's added.
export function GET() {
  const paths = ['/', ...docPages.map((page) => page.path)];
  const urls = paths.map((path) => `  <url><loc>${siteURL}${path}</loc></url>`).join('\n');
  const body = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${urls}
</urlset>
`;
  return new Response(body, { headers: { 'content-type': 'application/xml; charset=utf-8' } });
}
