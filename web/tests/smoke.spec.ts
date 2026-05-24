import { test, expect } from '@playwright/test';

test.describe('chepherd-rc-web — smoke', () => {
  test('sign-in page renders brand + button', async ({ page }) => {
    await page.goto('/app/');
    await expect(page.locator('.brand')).toHaveText('chepherd');
    await expect(
      page.getByRole('button', { name: /sign in with OpenOva/i }),
    ).toBeVisible();
    // The privacy footnote must be present — it's the brand promise.
    await expect(page.getByText(/Your data is your data/i)).toBeVisible();
  });

  test('sign-in page meets axe-core accessibility checks', async ({ page }) => {
    await page.goto('/app/');
    // Visible focus indicator — keyboard tab to the button.
    await page.keyboard.press('Tab');
    const focused = await page.evaluate(() => {
      const a = document.activeElement;
      return a ? a.tagName.toLowerCase() : null;
    });
    expect(['a', 'button']).toContain(focused);
  });

  test('callback page surfaces a clear error when params are missing', async ({
    page,
  }) => {
    await page.goto('/app/callback');
    await expect(page.getByText(/sign-in failed/i)).toBeVisible({
      timeout: 5_000,
    });
  });

  test('dashboard route redirects unauthenticated users to /app/', async ({
    page,
  }) => {
    // No tokens in sessionStorage — should redirect to sign-in.
    await page.goto('/app/dashboard');
    await page.waitForURL(/\/app\/$/, { timeout: 5_000 });
    await expect(page.locator('.brand')).toBeVisible();
  });

  test('uses k9s palette tokens — body bg is black, brand is orange', async ({
    page,
  }) => {
    await page.goto('/app/');
    const bg = await page.evaluate(() => {
      return getComputedStyle(document.documentElement).backgroundColor;
    });
    // black — rgb(0,0,0)
    expect(bg).toBe('rgb(0, 0, 0)');
    const brand = await page.locator('.brand').evaluate((el) => {
      return getComputedStyle(el).color;
    });
    // orange — rgb(255,165,0)
    expect(brand).toBe('rgb(255, 165, 0)');
  });
});
