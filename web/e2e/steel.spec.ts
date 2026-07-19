import { test, expect, chromium, type Browser, type Page } from '@playwright/test';

// Phase 0 acceptance: two clients join the same room and each renders the
// other's video with real frames (fake media devices, real SFU path).
test('two participants exchange video through the SFU', async () => {
  const browser: Browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required'
    ]
  });
  const room = `steel-e2e-${Date.now()}`;

  const join = async (name: string): Promise<Page> => {
    const context = await browser.newContext({ permissions: ['camera', 'microphone'] });
    const page = await context.newPage();
    await page.goto('/dev');
    await page.fill('input[name="room"]', room);
    await page.fill('input[name="name"]', name);
    await page.click('button[type="submit"]');
    return page;
  };

  const alice = await join('alice');
  const bob = await join('bob');

  const expectRemoteFrames = async (page: Page, remoteName: string) => {
    const video = page.locator(`video[aria-label="${remoteName}'s video"]`);
    await expect(video).toBeVisible({ timeout: 20_000 });
    await expect
      .poll(() => video.evaluate((el) => (el as HTMLVideoElement).videoWidth), {
        timeout: 20_000
      })
      .toBeGreaterThan(0);
  };

  await expectRemoteFrames(alice, 'bob');
  await expectRemoteFrames(bob, 'alice');

  await browser.close();
});
