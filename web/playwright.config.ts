import { defineConfig, devices } from '@playwright/test';

// End-to-end test config for the chepherd dashboard.
// Spawns the chepherd-v05 runtime + Astro dev server on the same machine
// and runs Chromium against http://127.0.0.1:4321/app.
export default defineConfig({
  testDir: './tests',
  testMatch: '**/*.spec.ts',
  fullyParallel: false,        // tests share runtime state; serial
  workers: 1,
  retries: 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: 'http://127.0.0.1:4321',
    headless: true,
    viewport: { width: 1400, height: 900 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  // Devs must have both the dashboard runtime AND the dev server up
  // before running tests. In CI: spawn both via the run-tests script.
  webServer: process.env.CI
    ? {
        command: 'npm run dev',
        url: 'http://127.0.0.1:4321/app',
        reuseExistingServer: false,
        timeout: 30_000,
      }
    : undefined,
});
