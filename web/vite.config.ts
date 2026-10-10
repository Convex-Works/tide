import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, type Plugin } from 'vite';

// CI's browser specs need no hot reload, and the Forgejo runner's
// containers run out of file watches before Vite can start (EMFILE). Set
// after resolution: SvelteKit's plugin merges its own `server.watch` options
// over any `null` given in the config, and `null` is what turns watching off.
const noWatchInCI: Plugin = {
  name: 'tide:no-watch-in-ci',
  configResolved(config) {
    if (process.env.CI) (config.server as { watch?: null }).watch = null;
  }
};

export default defineConfig({
  plugins: [tailwindcss(), sveltekit(), noWatchInCI],
  server: {
    host: true,
    // Egress's headless Chrome loads /egress-template from inside Docker.
    allowedHosts: ['host.docker.internal'],
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
