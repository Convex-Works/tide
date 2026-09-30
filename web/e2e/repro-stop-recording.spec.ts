import { test, expect, chromium, type BrowserContext, type Page } from '@playwright/test';
import { createRoomAndGetSlug, dexLogin, joinAsGuestThroughLobby } from './helpers';

// Repro for: "ending the recording drops all the other feeds (audio+video)".
// Measures whether remote media keeps flowing after recording stop.

interface MediaProbe {
  connectionState: string;
  videos: {
    label: string;
    currentTime: number;
    readyState: number;
    trackState?: string;
    trackMuted?: boolean;
  }[];
  audios: { label: string; trackState?: string; trackMuted?: boolean }[];
  remoteParticipants: {
    identity: string;
    pubs: { source: string; subscribed: boolean; trackState?: string; trackMuted?: boolean }[];
  }[];
}

async function probe(page: Page): Promise<MediaProbe> {
  return page.evaluate(() => {
    const room = (window as any).__klisiRoom;
    const mediaTrack = (el: HTMLMediaElement) => {
      const stream = el.srcObject as MediaStream | null;
      const track = stream?.getTracks()[0];
      return { trackState: track?.readyState, trackMuted: track?.muted };
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
        ...mediaTrack(el)
      })),
      remoteParticipants: [...(room?.remoteParticipants?.values() ?? [])].map((p: any) => ({
        identity: p.identity,
        pubs: [...p.trackPublications.values()].map((pub: any) => ({
          source: pub.source,
          subscribed: pub.isSubscribed,
          trackState: pub.track?.mediaStreamTrack?.readyState,
          trackMuted: pub.track?.mediaStreamTrack?.muted
        }))
      }))
    };
  });
}

async function assertRemoteVideoAdvancing(page: Page, who: string): Promise<void> {
  const before = await probe(page);
  await page.waitForTimeout(1500);
  const after = await probe(page);
  for (const videoAfter of after.videos) {
    const videoBefore = before.videos.find((v) => v.label === videoAfter.label);
    expect(
      videoAfter.currentTime,
      `${who}: video "${videoAfter.label}" should be advancing. before=${JSON.stringify(videoBefore)} after=${JSON.stringify(videoAfter)}`
    ).toBeGreaterThan(videoBefore?.currentTime ?? 0);
  }
}

test('remote feeds survive a recording stop', async () => {
  test.setTimeout(240_000);
  const browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required'
    ]
  });
  const contexts: BrowserContext[] = [];
  const newContext = async (): Promise<BrowserContext> => {
    const context = await browser.newContext({
      baseURL: 'http://localhost:5173',
      permissions: ['camera', 'microphone']
    });
    contexts.push(context);
    return context;
  };

  try {
    const hostContext = await newContext();
    const host = await hostContext.newPage();
    host.on('console', (msg) => {
      if (msg.type() === 'error' || msg.type() === 'warning')
        console.log(`[host console] ${msg.type()}: ${msg.text()}`);
    });
    await dexLogin(host);
    const slug = await createRoomAndGetSlug(host, `Stop repro ${Date.now()}`);
    await host.goto(`/m/${slug}`);
    await host.getByRole('button', { name: 'Join meeting' }).click();
    await expect(host.getByRole('button', { name: 'Start recording' })).toBeVisible({
      timeout: 20_000
    });

    const guest = await (await newContext()).newPage();
    guest.on('console', (msg) => {
      if (msg.type() === 'error' || msg.type() === 'warning')
        console.log(`[guest console] ${msg.type()}: ${msg.text()}`);
    });
    await joinAsGuestThroughLobby(guest, host, slug, 'Repro guest');
    await expect(host.locator(`video[aria-label="Repro guest's video"]`)).toBeVisible({
      timeout: 20_000
    });
    await expect(guest.locator('video[aria-label$="video"]')).toHaveCount(2, { timeout: 20_000 });

    console.log('--- baseline (before recording) ---');
    await assertRemoteVideoAdvancing(host, 'host');
    await assertRemoteVideoAdvancing(guest, 'guest');

    await host.getByRole('button', { name: 'Start recording' }).click();
    const startedResponsePromise = host.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        response.url().endsWith(`/api/rooms/${slug}/recording/start`),
      { timeout: 20_000 }
    );
    await host.getByRole('button', { name: 'Record?' }).click();
    const startedResponse = await startedResponsePromise;
    if (startedResponse.status() >= 500) {
      test.skip(true, 'LiveKit Egress is unhealthy or unavailable in this environment.');
    }
    expect(startedResponse.status()).toBe(201);

    await expect
      .poll(
        async () => {
          const list = await host.request.get(`/api/rooms/${slug}/recordings`);
          const items = (await list.json()) as { status: string }[];
          return items[0]?.status ?? 'none';
        },
        { timeout: 90_000 }
      )
      .toBe('recording');
    await expect(guest.getByTestId('recording-chip')).toHaveText('REC', { timeout: 20_000 });

    console.log('--- during recording ---');
    await assertRemoteVideoAdvancing(host, 'host');
    await assertRemoteVideoAdvancing(guest, 'guest');
    console.log('host during:', JSON.stringify(await probe(host)));
    console.log('guest during:', JSON.stringify(await probe(guest)));

    await host.waitForTimeout(6_000);
    await host.getByRole('button', { name: 'Stop recording' }).click();
    await expect(host.getByTestId('recording-chip')).toHaveCount(0, { timeout: 20_000 });
    await expect(guest.getByTestId('recording-chip')).toHaveCount(0, { timeout: 20_000 });

    // Let the egress fully wind down (egress_ended webhook, hidden participant
    // departure, dynacast re-evaluation).
    await host.waitForTimeout(8_000);

    console.log('--- after stop ---');
    const hostAfter = await probe(host);
    const guestAfter = await probe(guest);
    console.log('host after:', JSON.stringify(hostAfter));
    console.log('guest after:', JSON.stringify(guestAfter));

    expect(hostAfter.connectionState, 'host should still be connected').toBe('connected');
    expect(guestAfter.connectionState, 'guest should still be connected').toBe('connected');

    await assertRemoteVideoAdvancing(host, 'host (after stop)');
    await assertRemoteVideoAdvancing(guest, 'guest (after stop)');

    for (const [who, snapshot] of [
      ['host', hostAfter],
      ['guest', guestAfter]
    ] as const) {
      for (const participant of snapshot.remoteParticipants) {
        for (const pub of participant.pubs) {
          expect(
            pub.trackState,
            `${who}: ${participant.identity} ${pub.source} track should be live: ${JSON.stringify(pub)}`
          ).toBe('live');
          expect(
            pub.trackMuted,
            `${who}: ${participant.identity} ${pub.source} track should not be muted: ${JSON.stringify(pub)}`
          ).toBe(false);
        }
      }
      for (const audio of snapshot.audios) {
        expect(
          audio.trackState,
          `${who}: audio element "${audio.label}" should have a live track`
        ).toBe('live');
      }
    }
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
    await browser.close();
  }
});
