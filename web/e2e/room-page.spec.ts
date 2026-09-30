import { expect, test } from '@playwright/test';
import { mockApi } from './mock-api';

// The dashboard and a room's page, against the Vite dev server with the API
// mocked in the browser.

test('the room link names its rules only once the link breaks them', async ({ page }) => {
  await mockApi(page);
  await page.goto('/rooms/standup');

  const link = page.getByRole('textbox', { name: /Custom room link/ });
  const status = page.locator('#room-slug-status');
  await expect(status).toHaveText('Meeting URL: /m/standup');

  await link.fill('ab');
  await link.blur();
  await expect(status).toHaveText('Use 3–64 lowercase letters, numbers, and single hyphens.');
  await expect(link).toHaveAttribute('aria-invalid', 'true');

  // Following the rules again brings the address back as it's typed.
  await link.fill('weekly-sync');
  await expect(status).toHaveText('Meeting URL: /m/weekly-sync');
  await expect(link).toHaveAttribute('aria-invalid', 'false');

  // Saving a broken link says why in the row, not in the browser's bubble.
  await link.fill('two--hyphens');
  await page.getByRole('button', { name: 'Save link' }).click();
  await expect(status).toHaveText('Use 3–64 lowercase letters, numbers, and single hyphens.');
});

test('the dashboard keeps a margin on a phone', async ({ page }) => {
  await mockApi(page);
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto('/');

  const card = page.getByTestId('room-card').first();
  await expect(card).toBeVisible();
  const box = await card.boundingBox();
  expect(box?.x).toBeGreaterThanOrEqual(16);
  expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(375 - 16);
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(
    false
  );
});
