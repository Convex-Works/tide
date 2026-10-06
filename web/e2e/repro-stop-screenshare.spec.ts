import { test, expect, chromium, type Browser, type Page } from '@playwright/test';

// Repro for: "ending the screen recording drops all the other feeds".
// Variant: ending a screen share swaps focus-layout -> grid, remounting every
// tile. Verifies remote audio+video survive the swap.

interface MediaProbe {
  connectionState: string;
  videos: { label: string; currentTime: number; readyState: number; trackState?: string }[];
  audios: { label: string; trackState?: string; paused?: boolean }[];
}

async function probe(page: Page): Promise<MediaProbe> {
  return page.evaluate(() => {
    const room = (window as any).__tideRoom;
    const mediaTrack = (el: HTMLMediaElement) => {
      const stream = el.srcObject as MediaStream | null;
      const track = stream?.getTracks()[0];
      return { trackState: track?.readyState };
    };
    return {
      connectionState: room?.state ?? 'no-room',
      videos: [...document.querySelectorAll('video')].map((el) => ({
        label: el.getAttribute('aria-label') ?? '',
        currentTime: el.currentTime,
        readyState: el.readyState,
        ...mediaTrack(el)
      })),
      audios: [...document.querySelectorAll('audio')].map((el) => ({
        label: el.getAttribute('aria-label') ?? '',
        paused: el.paused,
        ...mediaTrack(el)
      }))
    };
  });
}

async function assertRemoteMediaAlive(page: Page, who: string): Promise<void> {
  const before = await probe(page);
  await page.waitForTimeout(1500);
  const after = await probe(page);
  expect(after.connectionState, `${who} should still be connected`).toBe('connected');
  expect(after.videos.length, `${who} should still have video elements`).toBeGreaterThan(0);
  for (const videoAfter of after.videos) {
    const videoBefore = before.videos.find((v) => v.label === videoAfter.label);
    expect(
      videoAfter.currentTime,
      `${who}: video "${videoAfter.label}" should be advancing. before=${JSON.stringify(videoBefore)} after=${JSON.stringify(videoAfter)}`
    ).toBeGreaterThan(videoBefore?.currentTime ?? 0);
  }
  for (const audio of after.audios) {
    expect(
      audio.trackState,
      `${who}: audio "${audio.label}" should be live: ${JSON.stringify(audio)}`
    ).toBe('live');
    expect(audio.paused, `${who}: audio "${audio.label}" should be playing`).toBe(false);
  }
}

test('remote feeds survive starting and ending a screen share', async () => {
  test.setTimeout(120_000);
  const browser: Browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required',
      '--auto-select-desktop-capture-source=Entire'
    ]
  });
  const room = `stop-share-repro-${Date.now()}`;

  try {
    const join = async (name: string): Promise<Page> => {
      const context = await browser.newContext({
        baseURL: 'http://localhost:5173',
        permissions: ['camera', 'microphone']
      });
      const page = await context.newPage();
      page.on('console', (msg) => {
        if (msg.type() === 'error' || msg.type() === 'warning')
          console.log(`[${name} console] ${msg.type()}: ${msg.text()}`);
      });
      page.on('pageerror', (err) => console.log(`[${name} pageerror] ${err.message}`));
      await page.goto('/dev');
      await page.fill('input[name="room"]', room);
      await page.fill('input[name="name"]', name);
      await page.click('button[type="submit"]');
      await expect(page.getByRole('button', { name: 'Share screen' })).toBeVisible({
        timeout: 20_000
      });
      return page;
    };

    const alice = await join('alice');
    const bob = await join('bob');
    const carol = await join('carol');
    await expect(alice.locator(`video[aria-label="bob's video"]`)).toBeVisible({
      timeout: 20_000
    });
    await expect(alice.locator(`video[aria-label="carol's video"]`)).toBeVisible({
      timeout: 20_000
    });

    console.log('--- baseline ---');
    await assertRemoteMediaAlive(alice, 'alice');
    await assertRemoteMediaAlive(bob, 'bob');

    await alice.getByRole('button', { name: 'Share screen' }).click();
    await expect(bob.getByTestId('focus-pane')).toBeVisible({ timeout: 20_000 });
    await expect(bob.locator(`video[aria-label="alice's screen share"]`)).toBeVisible();
    await expect(alice.getByTestId('focus-pane')).toBeVisible({ timeout: 20_000 });

    console.log('--- during share ---');
    await assertRemoteMediaAlive(alice, 'alice (during)');
    await assertRemoteMediaAlive(bob, 'bob (during)');

    await alice.getByRole('button', { name: 'Stop sharing' }).click();
    await expect(bob.getByTestId('focus-pane')).toHaveCount(0, { timeout: 20_000 });
    await expect(alice.getByTestId('focus-pane')).toHaveCount(0, { timeout: 20_000 });
    await alice.waitForTimeout(2_000);

    console.log('--- after stop ---');
    console.log('alice after:', JSON.stringify(await probe(alice)));
    console.log('bob after:', JSON.stringify(await probe(bob)));
    console.log('carol after:', JSON.stringify(await probe(carol)));
    await assertRemoteMediaAlive(alice, 'alice (after)');
    await assertRemoteMediaAlive(bob, 'bob (after)');
    await assertRemoteMediaAlive(carol, 'carol (after)');
  } finally {
    await browser.close();
  }
});
