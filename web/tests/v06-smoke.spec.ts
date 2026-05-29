// v0.6 Playwright smoke tests. Run against the v0.6 dashboard at
// http://localhost:4322/v06 with backend on :8081.
//
//   npm run test:e2e:v06
//
// Requires both v0.6 runtime (port 8081) AND v0.6 dev server (port 4322)
// to be running. Use scripts/dev-restart-v06.sh + CHEPHERD_PORT=8081
// npm run dev -- --port 4322 in two terminals before invoking.
import { test, expect } from '@playwright/test';

const API_V06 = 'http://127.0.0.1:8081/api/v1';

async function resetV06(request) {
  const r = await request.get(`${API_V06}/sessions`);
  if (!r.ok()) return;
  const data = await r.json();
  for (const s of data.sessions || []) {
    if (s.name === 'shepherd') continue; // keep default shepherd
    await request.delete(`${API_V06}/sessions/${s.name}`);
  }
}

test.describe('v0.6 workspace canvas — end-to-end', () => {
  test.beforeEach(async ({ page, request }) => {
    await resetV06(request);
    await page.goto('http://localhost:4322/v06');
    await expect(page.locator('.brand')).toContainText('chepherd');
    await expect(page.locator('.brand .ver')).toContainText('v0.6');
  });

  test('default Focus layout renders with all expected widgets', async ({ page }) => {
    // Top bar
    await expect(page.locator('.topbar')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Focus' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Council' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Board' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Multi' })).toBeVisible();
    await expect(page.getByRole('button', { name: '+ spawn' })).toBeVisible();
    await expect(page.getByRole('button', { name: /templates/ })).toBeVisible();
    // Stats line shows agent/team/membership counts
    await expect(page.locator('.stats')).toContainText(/\d+ agents/);
  });

  test('view-switcher toggles between Focus / Council / Board / Multi', async ({ page }) => {
    for (const view of ['Council', 'Board', 'Multi', 'Focus']) {
      await page.getByRole('button', { name: view }).click();
      // Each layout has at least one pane visible after the switch
      await expect(page.locator('.pane').first()).toBeVisible({ timeout: 3000 });
    }
  });

  test('spawn modal opens + has fresh/resume toggle', async ({ page }) => {
    await page.getByRole('button', { name: '+ spawn' }).click();
    await expect(page.locator('.modal h2', { hasText: 'spawn agent' })).toBeVisible();
    await expect(page.locator('button', { hasText: 'Fresh session' })).toBeVisible();
    await expect(page.locator('button', { hasText: 'Resume previous Claude session' })).toBeVisible();
  });

  test('template picker lists available templates', async ({ page }) => {
    await page.getByRole('button', { name: /templates/ }).click();
    await expect(page.locator('.modal').filter({ hasText: 'install a team template' })).toBeVisible();
    // Should show at least 3 templates (solo / pair / council)
    await expect(page.locator('.picker li').filter({ hasText: /solo-supervised/ })).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.picker li').filter({ hasText: /^pair/ })).toBeVisible();
    await expect(page.locator('.picker li').filter({ hasText: /^council/ })).toBeVisible();
  });

  test('apply pair template via API → 3 agents spawned + auto-membership', async ({ page, request }) => {
    const r = await request.post(`${API_V06}/templates/pair/apply`, {
      data: { team: 'test-pair', cwd: '/tmp' },
    });
    expect(r.ok()).toBe(true);
    const data = await r.json();
    expect(data.members).toHaveLength(3);
    expect(data.members.map(m => m.Role).sort()).toEqual(['reviewer', 'shepherd', 'worker']);

    // Refresh dashboard + verify session-list shows new team
    await page.reload();
    await expect(page.locator('.team h3').filter({ hasText: 'test-pair' })).toBeVisible({ timeout: 5000 });
  });

  test('events strip populates from SSE stream', async ({ page, request }) => {
    // Spawn something to trigger an event
    await request.post(`${API_V06}/sessions`, {
      data: { name: 'evt-test', agent: 'sovereign-shell', team: 'default', role: 'worker', cwd: '/tmp' },
    });
    await page.reload();
    // The events widget should show the spawn within a few seconds
    await expect(
      page.locator('.events li').filter({ hasText: 'evt-test' }).first(),
    ).toBeVisible({ timeout: 8000 });
  });

  test('per-pane widget picker swaps widgets', async ({ page }) => {
    // Swap the first pane's widget to canon-viewer (single unique render)
    const picker = page.locator('.widget-pick').first();
    await picker.selectOption('canon-viewer');
    // After swap, the empty-state placeholder for canon-viewer renders
    await expect(page.locator('.pane').first()).toContainText(/widget: canon-viewer|Canon/);
  });

  test('pane split + close work', async ({ page }) => {
    const paneCountBefore = await page.locator('.pane').count();
    // Click the horizontal-split button on the first pane
    await page.locator('.pane-header button[title*="split horizontally"]').first().click();
    const paneCountAfter = await page.locator('.pane').count();
    expect(paneCountAfter).toBeGreaterThan(paneCountBefore);
    // Now close one pane
    await page.locator('.pane-header button[title="close"]').last().click();
    const paneCountFinal = await page.locator('.pane').count();
    expect(paneCountFinal).toBeLessThan(paneCountAfter);
  });
});
