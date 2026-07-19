// SPA mode: no SSR, no prerendering — adapter-static serves the fallback
// index.html for every route, including dynamic ones like /m/[slug].
export const ssr = false;
export const prerender = false;
