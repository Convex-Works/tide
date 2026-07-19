import { test, chromium, expect } from '@playwright/test';

// Capture the host's stage view at 3, 5, and 10 participants.
test('capture grid at 3, 5, 10 participants', async () => {
  test.setTimeout(300_000);
  const browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required'
    ]
  });

  for (const count of [3, 5, 10]) {
    const room = `grid-${count}-${Date.now()}`;
    const contexts = [];
    let hostPage;
    for (let i = 0; i < count; i++) {
      const context = await browser.newContext({
        permissions: ['camera', 'microphone'],
        viewport: { width: 1440, height: 900 }
      });
      contexts.push(context);
      const page = await context.newPage();
      await page.goto('/dev');
      await page.fill('input[name="room"]', room);
      await page.fill('input[name="name"]', i === 0 ? 'alice' : `guest-${i + 1}`);
      await page.click('button[type="submit"]');
      if (i === 0) hostPage = page;
    }
    await expect(hostPage!.locator('video')).toHaveCount(count, { timeout: 60_000 });
    await hostPage!.waitForTimeout(2500);
    await hostPage!.screenshot({ path: `shots/grid-${count}.png` });
    for (const context of contexts) await context.close();
  }
  await browser.close();
});
