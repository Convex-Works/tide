import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [
    tailwindcss(),
    sveltekit({
      // Every page is prerendered; 404.html is the client-rendered shell static
      // hosts serve for paths that don't exist.
      adapter: adapter({ fallback: '404.html' }),
      prerender: {
        handleHttpError: 'fail',
        handleMissingId: 'fail'
      }
    })
  ]
});
