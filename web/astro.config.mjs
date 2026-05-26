import { defineConfig } from 'astro/config';
import svelte from '@astrojs/svelte';

// Dev-server proxy.
//   /api/*       → :CHEPHERD_PORT  (default 8080 → v0.5 runtime)
//   /api-v06/*   → :CHEPHERD_PORT_V06 (default 8081 → v0.6 runtime)
//
// The two namespaces let a SINGLE Astro dev server (on :4321) serve both
// dashboards through one SSH tunnel. The /v06 page hits /api-v06/v1/...
// which always lands on the v0.6 runtime regardless of which port the
// page itself was served from. The /app page keeps using /api/v1/... so
// v0.5 is undisturbed.
//
// Production builds are static (served alongside the runtime so /api
// shares its origin) and ignore this block.
const backendPort = process.env.CHEPHERD_PORT || '8080';
const backendPortV06 = process.env.CHEPHERD_PORT_V06 || '8081';
const backendPortV07 = process.env.CHEPHERD_PORT_V07 || '8082';

export default defineConfig({
  integrations: [svelte()],
  output: 'static',
  site: 'https://chepherd.io',
  devToolbar: { enabled: false },
  vite: {
    server: {
      proxy: {
        // --- v0.7 (always :8082) ---
        '/api-v07/v1/sessions': {
          target: `ws://127.0.0.1:${backendPortV07}`,
          ws: true,
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/api-v07/, '/api'),
        },
        '/api-v07/v1/events/stream': {
          target: `http://127.0.0.1:${backendPortV07}`,
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/api-v07/, '/api'),
        },
        '/api-v07': {
          target: `http://127.0.0.1:${backendPortV07}`,
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/api-v07/, '/api'),
        },
        // --- v0.6 (always :8081, path rewrite strips "-v06") ---
        '/api-v06/v1/sessions': {
          target: `ws://127.0.0.1:${backendPortV06}`,
          ws: true,
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/api-v06/, '/api'),
        },
        '/api-v06/v1/events/stream': {
          target: `http://127.0.0.1:${backendPortV06}`,
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/api-v06/, '/api'),
        },
        '/api-v06': {
          target: `http://127.0.0.1:${backendPortV06}`,
          changeOrigin: true,
          rewrite: (p) => p.replace(/^\/api-v06/, '/api'),
        },
        // --- v0.5 (default :8080) ---
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
