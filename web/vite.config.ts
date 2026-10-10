import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  server: {
    host: true,
    // Egress's headless Chrome loads /egress-template from inside Docker.
    allowedHosts: ['host.docker.internal'],
    // CI's browser specs need no hot reload, and the Forgejo runner's
    // containers run out of file watches before Vite can start (EMFILE).
    watch: process.env.CI ? null : undefined,
    proxy: {
      '/api': 'http://localhost:8080',
      // moil machines pair and keep their WebSocket here (ARCHITECTURE.md §8.1).
      '/moil': { target: 'http://localhost:8080', ws: true },
      // Meeting signaling: tide forwards it to its media server, so the SPA
      // signals through its own origin here too (ARCHITECTURE.md §2.1, §13).
      '/rtc': { target: 'http://localhost:8080', ws: true }
    }
  }
});
