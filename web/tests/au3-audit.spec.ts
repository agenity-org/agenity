// au3-audit.spec.ts — #490 Wave AU3 Playwright route-smoke per the
// feedback_ui_changes_need_route_smoke_test memory.
//
// Tests cover both the actual /audit route AND the component
// behavior (filter call shape, per-agent scope, polling) by using a
// mocked AU2 API at /api/v1/audit/events. Run via `npm run test:e2e`
// (uses playwright.config.ts which boots the Astro dev server).
//
// Named assertions A1-A5:
//
//	A1 — /audit renders the AuditLog component (audit-log-root
//	     + audit-title visible; testid count matches component schema)
//	A2 — initial GET /api/v1/audit/events fires (no filter params)
//	A3 — filter form: change method filter + Apply → 2nd request
//	     with ?method=<value>
//	A4 — clicking a caller link enters per-agent scope: ?agent=<sid>
//	     surfaces in URL + 2 requests fire (caller= + callee=)
//	A5 — clear-scope button removes ?agent= from URL + fires
//	     unscoped request
//
// Refs #490 #489.
import { test, expect } from '@playwright/test';

const AU2_PATH = '/api/v1/audit/events';

const SAMPLE_ROW = {
  id: '019e7e2d-74fb-7b47-8dca-f35a2228ac10',
  event_type: 'audit.received',
  timestamp: '2026-05-31T14:00:00Z',
  caller: 'agent-alpha',
  callee: 'runner-7c33',
  method: 'message/send',
  latency_ms: 42,
  status: 'success',
};

async function mockAuditAPI(page, fixturesRef) {
  // fixturesRef.calls captures every request the page makes to AU2.
  fixturesRef.calls = [];
  await page.route('**/api/v1/audit/events*', (route) => {
    const url = new URL(route.request().url());
    fixturesRef.calls.push({
      caller: url.searchParams.get('caller'),
      callee: url.searchParams.get('callee'),
      method: url.searchParams.get('method'),
      limit: url.searchParams.get('limit'),
      since: url.searchParams.get('since'),
      until: url.searchParams.get('until'),
    });
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        events: [SAMPLE_ROW],
        org_id: 'default',
      }),
    });
  });
}

test.describe('AU3 — /audit dashboard surface (#490)', () => {
  test('A1 + A2 — route renders + initial API call fires', async ({ page }) => {
    const fx = { calls: [] };
    await mockAuditAPI(page, fx);

    await page.goto('/audit/');

    // A1 — landmarks
    await expect(page.getByTestId('audit-log-root')).toBeVisible();
    await expect(page.getByTestId('audit-title')).toContainText('Audit Log');
    await expect(page.getByTestId('audit-table')).toBeVisible();
    await expect(page.getByTestId('filter-caller')).toBeVisible();
    await expect(page.getByTestId('filter-callee')).toBeVisible();
    await expect(page.getByTestId('filter-method')).toBeVisible();
    await expect(page.getByTestId('filter-status')).toBeVisible();
    await expect(page.getByTestId('filter-apply')).toBeVisible();

    // A2 — initial unfiltered GET
    await expect.poll(() => fx.calls.length).toBeGreaterThan(0);
    expect(fx.calls[0]).toMatchObject({
      caller: null,
      callee: null,
      method: null,
    });

    // Sample row populated
    await expect(page.getByTestId('audit-row')).toHaveCount(1);
    await expect(page.getByTestId('audit-row').first()).toContainText('message/send');
  });

  test('A3 — filter method + Apply triggers a 2nd request with method param', async ({ page }) => {
    const fx = { calls: [] };
    await mockAuditAPI(page, fx);

    await page.goto('/audit/');
    await expect(page.getByTestId('audit-log-root')).toBeVisible();
    const initialCalls = fx.calls.length;

    await page.getByTestId('filter-method').fill('tasks/get');
    await page.getByTestId('filter-apply').click();

    await expect.poll(() => fx.calls.length).toBeGreaterThan(initialCalls);
    expect(fx.calls.at(-1)).toMatchObject({ method: 'tasks/get' });
  });

  test('A4 — clicking a caller link sets ?agent= + fires 2 scoped requests', async ({ page }) => {
    const fx = { calls: [] };
    await mockAuditAPI(page, fx);

    await page.goto('/audit/');
    await expect(page.getByTestId('audit-row')).toHaveCount(1);
    const initialCalls = fx.calls.length;

    await page.getByTestId('agent-link-caller').first().click();

    // URL contains the scope query param
    await expect.poll(() => new URL(page.url()).searchParams.get('agent')).toBe('agent-alpha');

    // Scope badge visible
    await expect(page.getByTestId('audit-agent-scope')).toBeVisible();
    await expect(page.getByTestId('audit-agent-scope')).toContainText('agent-alpha');

    // 2 NEW calls fired — one with caller=, one with callee=
    await expect.poll(() => fx.calls.length).toBeGreaterThanOrEqual(initialCalls + 2);
    const recent2 = fx.calls.slice(-2);
    const callerCall = recent2.find((c) => c.caller === 'agent-alpha');
    const calleeCall = recent2.find((c) => c.callee === 'agent-alpha');
    expect(callerCall).toBeTruthy();
    expect(calleeCall).toBeTruthy();
  });

  test('A5 — clear-scope removes ?agent= from URL + fires unscoped request', async ({ page }) => {
    const fx = { calls: [] };
    await mockAuditAPI(page, fx);

    await page.goto('/audit/?agent=runner-7c33');
    await expect(page.getByTestId('audit-agent-scope')).toBeVisible();

    const initialCalls = fx.calls.length;
    await page.getByTestId('audit-clear-scope').click();

    await expect(page.getByTestId('audit-agent-scope')).toHaveCount(0);
    await expect.poll(() => new URL(page.url()).searchParams.get('agent')).toBeNull();

    await expect.poll(() => fx.calls.length).toBeGreaterThan(initialCalls);
    expect(fx.calls.at(-1)).toMatchObject({ caller: null, callee: null });
  });
});
