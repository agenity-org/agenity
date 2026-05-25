import { defineConfig } from 'astro/config';
import svelte from '@astrojs/svelte';

// Dev-server proxy. Backend port is configurable via CHEPHERD_PORT env
// var (default 8080 = v0.5). Set CHEPHERD_PORT=8081 to point the dev
// server at the v0.6 runtime.
//
//   CHEPHERD_PORT=8080 npm run dev       # v0.5 dashboard (default)
//   CHEPHERD_PORT=8081 npm run dev -- --port 4322   # v0.6 dashboard
//
// Production builds are static (served alongside the runtime so /api
// shares its origin) and ignore this block.
const backendPort = process.env.CHEPHERD_PORT || '8080';

export default defineConfig({
  integrations: [svelte()],
  output: 'static',
  site: 'https://chepherd.io',
  devToolbar: { enabled: false },
  vite: {
    server: {
      proxy: {
        '/api/v1/sessions': {
          target: `ws://127.0.0.1:${backendPort}`,
          ws: true,
          changeOrigin: true,
        },
        '/api/v1/events/stream': {
          target: `http://127.0.0.1:${backendPort}`,
          changeOrigin: true,
        },
        '/api': {
          target: `http://127.0.0.1:${backendPort}`,
          changeOrigin: true,
        },
      },
    },
  },
});
