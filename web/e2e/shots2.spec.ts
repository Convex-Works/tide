import { test, chromium } from '@playwright/test';

// One-off design review captures for Phase 2 surfaces.
test('capture dashboard and lobby states', async () => {
  test.setTimeout(120_000);
  const browser = await chromium.launch({
    args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream']
  });
  const host = await browser.newContext({
    permissions: ['camera', 'microphone'],
    viewport: { width: 1280, height: 800 }
  });
  const page = await host.newPage();
  await page.goto('/');
  await page.waitForTimeout(800);
  await page.screenshot({ path: 'shots/signin.png' });

  await page.getByRole('button', { name: /continue with sso/i }).click();
  await page.fill('input[name="login"]', 'host@tide.dev');
  await page.fill('input[name="password"]', 'tide-dev');
  await page.click('button[type="submit"]');
  await page.waitForURL('http://localhost:5173/**', { timeout: 20_000 });
  await page.waitForTimeout(800);
  await page.screenshot({ path: 'shots/dashboard.png' });

  const guest = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  // The redesigned room card carries the slug only in its Join link href.
  const joinLink = page.locator('[data-testid="room-card"]').getByRole('link', { name: 'Join' });
  const slug =
    (await joinLink.count()) > 0
      ? (await joinLink.first().getAttribute('href'))?.replace('/m/', '').trim()
      : undefined;
  if (slug) {
    const gp = await guest.newPage();
    await gp.goto(`/m/${slug}`);
    await gp.waitForTimeout(1200);
    await gp.screenshot({ path: 'shots/prejoin-slug.png' });
  }
  await browser.close();
});
