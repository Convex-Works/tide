import { test, expect, chromium } from '@playwright/test';

// Phase 1 acceptance, split into its two deterministic halves:
// 1. A dropped signal connection recovers without a page reload
//    (simulateScenario 'signal-reconnect'; when livekit-client resumes
//    instantly no intermediate state is observable — that IS the desired
//    behavior, so the assertion is survival, not choreography).
// 2. The hairline renders every chrome state, driven through the store
//    hook — the UI mapping cannot race the resume speed.
test('signal loss recovers without reload and the hairline maps all states', async () => {
  const browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required'
    ]
  });
  const context = await browser.newContext({ permissions: ['camera', 'microphone'] });
  const page = await context.newPage();
  await page.goto('/dev');
  await page.fill('input[name="room"]', `reconnect-${Date.now()}`);
  await page.fill('input[name="name"]', 'alice');
  await page.click('button[type="submit"]');

  // The hairline's idle state is also "connected", and it renders on the
  // prejoin screen — waiting on it alone races Room.connect(): a simulate
  // fired mid-join aborts the join permanently. Gate on the control bar,
  // which only exists once the stage is truly connected.
  await expect(page.getByRole('button', { name: 'Mute microphone' })).toBeVisible({
    timeout: 20_000
  });
  const hairline = page.locator('[data-testid="hairline"]');
  await expect(hairline).toHaveAttribute('data-state', 'connected', { timeout: 15_000 });

  // Reload detection: this marker only survives if the document does.
  await page.evaluate(() => {
    (window as never as { __noReload: boolean }).__noReload = true;
    const room = (window as never as { __tideRoom?: { simulateScenario(s: string): void } })
      .__tideRoom;
    if (!room) throw new Error('__tideRoom missing');
    room.simulateScenario('signal-reconnect');
  });

  await expect
    .poll(
      () =>
        page.evaluate(() => {
          const w = window as never as {
            __noReload?: boolean;
            __tideRoom?: { state: string };
          };
          return `${w.__noReload === true}:${w.__tideRoom?.state}`;
        }),
      { timeout: 45_000 }
    )
    .toBe('true:connected');

  // UI mapping, driven deterministically through the store hook.
  for (const state of ['reconnecting', 'offline', 'recording', 'connected']) {
    await page.evaluate(
      (s) => (window as never as { __tideChrome: (s: string) => void }).__tideChrome(s),
      state
    );
    await expect(hairline).toHaveAttribute('data-state', state);
  }

  await browser.close();
});
