import { test, expect, chromium, type Browser } from '@playwright/test';

test('microphone control updates UI and the local publication', async () => {
  const browser: Browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required'
    ]
  });

  try {
    const context = await browser.newContext({ permissions: ['camera', 'microphone'] });
    const page = await context.newPage();
    await page.goto('/dev');
    await page.fill('input[name="room"]', `controls-e2e-${Date.now()}`);
    await page.fill('input[name="name"]', 'alice');
    await page.click('button[type="submit"]');

    const microphone = page.getByRole('button', { name: 'Mute microphone' });
    await expect(microphone).toBeVisible({ timeout: 20_000 });
    await expect(microphone).toHaveAttribute('aria-pressed', 'true');

    // A local microphone must never be attached to a playback element. LiveKit
    // unmutes audio elements while attaching tracks, which can otherwise play
    // the participant's own microphone briefly during room entry.
    await expect(page.locator(`audio[aria-label="alice's audio"]`)).toHaveCount(0);

    await microphone.click();
    await expect(page.getByRole('button', { name: 'Unmute microphone' })).toHaveAttribute(
      'aria-pressed',
      'false'
    );

    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const room = (
              window as Window & {
                __klisiRoom?: {
                  localParticipant: {
                    audioTrackPublications: Map<string, { source: string; isMuted: boolean }>;
                  };
                };
              }
            ).__klisiRoom;
            const publication = room
              ? [...room.localParticipant.audioTrackPublications.values()].find(
                  (candidate) => candidate.source === 'microphone'
                )
              : undefined;
            return publication?.isMuted ?? false;
          }),
        { timeout: 10_000 }
      )
      .toBe(true);
  } finally {
    await browser.close();
  }
});

test('device menus list and switch cameras and microphones in-call', async () => {
  const browser: Browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required'
    ]
  });

  try {
    const context = await browser.newContext({ permissions: ['camera', 'microphone'] });
    const page = await context.newPage();
    await page.goto('/dev');
    await page.fill('input[name="room"]', `devices-e2e-${Date.now()}`);
    await page.fill('input[name="name"]', 'alice');
    await page.click('button[type="submit"]');
    await expect(page.getByRole('button', { name: 'Mute microphone' })).toBeVisible({
      timeout: 20_000
    });

    // Camera menu opens, lists the fake device, and shows the active check.
    await page.getByRole('button', { name: 'Choose camera' }).click();
    const cameraMenu = page.getByRole('menu', { name: 'Choose camera' });
    await expect(cameraMenu).toBeVisible();
    const cameraOptions = cameraMenu.getByRole('menuitemradio');
    await expect
      .poll(async () => cameraOptions.count(), { timeout: 10_000 })
      .toBeGreaterThanOrEqual(1);

    // Picking a device closes the menu and keeps local video flowing.
    await cameraOptions.first().click();
    await expect(cameraMenu).not.toBeVisible();
    const localVideo = page.locator(`video[aria-label="alice's video"]`);
    await expect
      .poll(() => localVideo.evaluate((el) => (el as HTMLVideoElement).videoWidth), {
        timeout: 10_000
      })
      .toBeGreaterThan(0);

    // Microphone menu works the same way and closes on Escape.
    await page.getByRole('button', { name: 'Choose microphone' }).click();
    const microphoneMenu = page.getByRole('menu', { name: 'Choose microphone' });
    await expect(microphoneMenu).toBeVisible();
    await expect
      .poll(async () => microphoneMenu.getByRole('menuitemradio').count(), { timeout: 10_000 })
      .toBeGreaterThanOrEqual(1);
    await page.keyboard.press('Escape');
    await expect(microphoneMenu).not.toBeVisible();
  } finally {
    await browser.close();
  }
});
