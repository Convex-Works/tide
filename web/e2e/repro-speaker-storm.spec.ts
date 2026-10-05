import { test, expect, chromium, type Browser, type Page } from '@playwright/test';

// Proves/refutes: rapid ActiveSpeakersChanged events (real conversation) cause
// syncParticipants() rebuilds -> attachTrack detach/re-attach churn -> media
// elements wedge. This is what live speech does that fake-media e2e never does.

async function snapshot(page: Page) {
  return page.evaluate(() => ({
    videos: [...document.querySelectorAll('video')].map((el) => ({
      label: el.getAttribute('aria-label') ?? '',
      currentTime: el.currentTime,
      paused: el.paused,
      readyState: el.readyState
    })),
    audios: [...document.querySelectorAll('audio')].map((el) => ({
      label: el.getAttribute('aria-label') ?? '',
      paused: el.paused
    }))
  }));
}

test('remote feeds survive an active-speaker event storm', async () => {
  test.setTimeout(120_000);
  const browser: Browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required'
    ]
  });
  const room = `speaker-storm-${Date.now()}`;

  try {
    const join = async (name: string): Promise<Page> => {
      const context = await browser.newContext({
        baseURL: 'http://localhost:5173',
        permissions: ['camera', 'microphone']
      });
      const page = await context.newPage();
      page.on('console', (msg) => {
        if (msg.type() === 'error') console.log(`[${name} console] ${msg.text()}`);
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
    await expect(alice.locator(`video[aria-label="bob's video"]`)).toBeVisible({
      timeout: 20_000
    });
    await expect(bob.locator(`video[aria-label="alice's video"]`)).toBeVisible({
      timeout: 20_000
    });
    await alice.waitForTimeout(2_000);

    console.log('before storm:', JSON.stringify(await snapshot(alice)));

    // Simulate a lively conversation: the speaker set flips ~6x/second for 6s,
    // as LiveKit reports alternating speech.
    await alice.evaluate(async () => {
      const room = (window as any).__tideRoom;
      const remotes = [...room.remoteParticipants.values()];
      const local = room.localParticipant;
      for (let i = 0; i < 40; i++) {
        const speakers = i % 2 === 0 ? [local] : [remotes[0]];
        room.emit('activeSpeakersChanged', speakers);
        await new Promise((resolve) => setTimeout(resolve, 150));
      }
      room.emit('activeSpeakersChanged', []);
    });

    // Regression guard: the storm must not have torn down the media elements.
    // (Before the attachTrack fix, every video sat at readyState 0 here.)
    const midStorm = await snapshot(alice);
    console.log('right after storm:', JSON.stringify(midStorm));
    for (const video of midStorm.videos) {
      expect(
        video.readyState,
        `alice: video "${video.label}" was reset by speaker events: ${JSON.stringify(video)}`
      ).toBeGreaterThanOrEqual(2);
    }

    // Give playback a chance to settle, then verify media is still advancing.
    await alice.waitForTimeout(2_000);
    const before = await snapshot(alice);
    await alice.waitForTimeout(1500);
    const after = await snapshot(alice);
    console.log('settled before:', JSON.stringify(before));
    console.log('settled after:', JSON.stringify(after));

    for (const videoAfter of after.videos) {
      const videoBefore = before.videos.find((v) => v.label === videoAfter.label);
      expect(
        videoAfter.currentTime,
        `alice: video "${videoAfter.label}" should still be advancing after the storm. before=${JSON.stringify(videoBefore)} after=${JSON.stringify(videoAfter)}`
      ).toBeGreaterThan(videoBefore?.currentTime ?? 0);
      expect(videoAfter.paused, `alice: video "${videoAfter.label}" should not be paused`).toBe(
        false
      );
    }
    for (const audio of after.audios) {
      expect(audio.paused, `alice: audio "${audio.label}" should not be paused`).toBe(false);
    }
  } finally {
    await browser.close();
  }
});
