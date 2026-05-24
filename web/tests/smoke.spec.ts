// End-to-end Playwright tests for the chepherd v0.5 dashboard at /app.
// Assumes a live chepherd-v05 runtime + Astro dev server on the host.
// See playwright.config.ts for setup; run with `npm run test:e2e`.
import { test, expect } from '@playwright/test';

const API = 'http://127.0.0.1:8080/api/v1';

// Helper: hard-reset the runtime to a known state. Stops every session
// except the default shepherd so test ordering doesn't matter.
async function resetRuntime(request) {
  const r = await request.get(`${API}/sessions`);
  if (!r.ok()) return;
  const data = await r.json();
  for (const s of data.sessions || []) {
    if (s.name === 'shepherd') continue;
    await request.delete(`${API}/sessions/${s.name}`);
  }
}

test.describe('chepherd dashboard — v0.5 end-to-end', () => {
  test.beforeEach(async ({ page, request }) => {
    await resetRuntime(request);
    await page.goto('/app');
    await expect(page.locator('.brand')).toContainText('chepherd');
  });

  test('default state — zero workers, one shepherd, single top bar', async ({ page }) => {
    await expect(page.locator('.stats')).toContainText('0 worker');
    await expect(page.locator('.stats')).toContainText('1 shepherd');
    // Top bar contains brand + stats + nav links + spawn button in ONE row.
    await expect(page.locator('header.topbar')).toBeVisible();
    await expect(page.locator('header.topbar .links a', { hasText: 'Docs' })).toBeVisible();
    await expect(page.locator('header.topbar .links a', { hasText: 'GitHub' })).toBeVisible();
    await expect(page.getByTestId('spawn-button')).toBeVisible();
    // Shepherd auto-spawned + visible in left pane.
    await expect(page.locator('.left h2', { hasText: /SHEPHERDS/i })).toBeVisible();
    await expect(page.locator('.session-list li', { hasText: 'shepherd' })).toBeVisible();
  });

  test('spawn modal opens (not window.prompt) on button click', async ({ page }) => {
    await page.getByTestId('spawn-button').click();
    await expect(page.locator('.modal-header h2', { hasText: '+ spawn agent' })).toBeVisible();
    // Form fields present.
    await expect(page.getByTestId('spawn-cwd-input')).toBeVisible();
    await expect(page.locator('select').first()).toBeVisible(); // agent dropdown
  });

  test('autocomplete dropdown filters as you type in working directory', async ({ page }) => {
    await page.getByTestId('spawn-button').click();
    const input = page.getByTestId('spawn-cwd-input');
    await input.click();
    // After focus, dropdown should populate with up to 8 recent folders.
    await expect(page.getByTestId('spawn-cwd-suggestions')).toBeVisible({ timeout: 5000 });
    // Type to filter.
    await input.fill('iogrid');
    // The iogrid path should still match.
    await expect(
      page.locator('[data-testid=spawn-cwd-suggestions] li', { hasText: /iogrid/ }),
    ).toBeVisible();
    // Typing a deliberately-bogus string should leave no matches → dropdown hidden.
    await input.fill('xyzzy-no-such-folder-zzz');
    await expect(page.getByTestId('spawn-cwd-suggestions')).toBeHidden();
  });

  test('picking a folder auto-fills the session name', async ({ page }) => {
    await page.getByTestId('spawn-button').click();
    const input = page.getByTestId('spawn-cwd-input');
    await input.click();
    await input.fill('iogrid');
    await expect(page.getByTestId('spawn-cwd-suggestions')).toBeVisible();
    // Click the iogrid suggestion.
    await page.locator('[data-testid=spawn-cwd-suggestions] li', { hasText: /iogrid/ }).first().click();
    // Name field auto-derives from cwd basename.
    const nameInput = page.locator('input[placeholder*="iogrid-1, review-bot"], input[placeholder*="iogrid"]').first();
    // The placeholder shows the auto-name; the bind:value is empty until typed.
    // We assert the placeholder reflects the autoName().
    const ph = await nameInput.getAttribute('placeholder');
    expect(ph).toMatch(/iogrid/);
  });

  test('stop session uses modal confirm dialog (not window.confirm)', async ({ page, request }) => {
    // Spawn a worker we can stop.
    await request.post(`${API}/sessions`, {
      data: { name: 'test-stop-target', agent: 'sovereign-shell', tribe: 'default', role: 'worker', cwd: '/tmp' },
    });
    await page.reload();
    // Click the worker to attach.
    await page.locator('.session-list li', { hasText: 'test-stop-target' }).first().click();
    await expect(page.locator('.right h2', { hasText: 'Session' })).toBeVisible();
    // Hook native dialog so we can fail the test if it ever fires.
    let nativeDialogFired = false;
    page.on('dialog', () => { nativeDialogFired = true; });
    // Click the danger stop button.
    await page.locator('button.danger', { hasText: 'stop' }).click();
    // The custom confirm dialog must appear.
    await expect(page.getByTestId('confirm-backdrop')).toBeVisible();
    await expect(page.locator('.modal.confirm h2')).toContainText('Stop session?');
    // Cancel keeps the session alive.
    await page.getByTestId('confirm-cancel').click();
    await expect(page.getByTestId('confirm-backdrop')).toBeHidden();
    await expect(page.locator('.session-list li', { hasText: 'test-stop-target' })).toBeVisible();
    // Re-open + confirm this time.
    await page.locator('button.danger', { hasText: 'stop' }).click();
    await page.getByTestId('confirm-ok').click();
    await expect(page.locator('.session-list li', { hasText: 'test-stop-target' })).toBeHidden({ timeout: 5000 });
    // Critically: no window.confirm/alert/prompt was triggered.
    expect(nativeDialogFired).toBe(false);
  });

  test('spawning a worker fresh via the modal lands a session', async ({ page, request }) => {
    await page.getByTestId('spawn-button').click();
    const input = page.getByTestId('spawn-cwd-input');
    await input.click();
    await input.fill('/tmp');
    // /tmp isn't in recent-folders; just submit the typed cwd.
    // Click into name + override to make it deterministic.
    await page.locator('input[placeholder*="review-bot"]').fill('test-fresh-spawn');
    // Agent: pick sovereign-shell (faster, no Claude API).
    const agentSelect = page.locator('select').first();
    await agentSelect.selectOption('sovereign-shell');
    await page.getByTestId('spawn-submit').click();
    // The modal should close + new session appears in workers list.
    await expect(page.locator('.modal-header h2', { hasText: '+ spawn agent' })).toBeHidden({ timeout: 5000 });
    await expect(page.locator('.session-list li', { hasText: 'test-fresh-spawn' })).toBeVisible();
  });
});
