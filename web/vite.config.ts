import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  server: {
    host: true,
    // Egress's headless Chrome loads /egress-template from inside Docker.
    allowedHosts: ['host.docker.internal'],
    proxy: {
      '/api': 'http://localhost:8080'
    }
  }
});
