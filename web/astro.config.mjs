import { defineConfig } from 'astro/config';
import svelte from '@astrojs/svelte';

// Dev-server proxy: forward /api/v1 + WebSocket attaches to the local
// chepherd-v05 runtime on :8080. Production builds are static (served
// alongside the runtime so /api shares its origin) and ignore this block.
export default defineConfig({
  integrations: [svelte()],
  output: 'static',
  site: 'https://chepherd.io',
  vite: {
    server: {
      proxy: {
        '/api/v1/sessions': {
          target: 'ws://127.0.0.1:8080',
          ws: true,
          changeOrigin: true,
        },
        '/api': {
          target: 'http://127.0.0.1:8080',
          changeOrigin: true,
        },
      },
    },
  },
});
