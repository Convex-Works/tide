import { chromium, expect, test, type Browser, type Page } from '@playwright/test';
import {
  attachDiagnostics,
  captureBrowserDiagnostics,
  expectMediaElementTags,
  expectMediaInvariant,
  joinMediaTestRoom,
  probe,
  publishSyntheticCameraAndAudio,
  publishSyntheticScreen,
  resubscribeSettleTimeout,
  tagMediaElements,
  unpublishSynthetic
} from './media-helpers';

test('media ownership converges across the deterministic lifecycle corpus', async ({
  page
}, testInfo) => {
  test.setTimeout(180_000);
  const roomName = `media-gate-${testInfo.project.name}-${Date.now()}`;
  const receiverConsole = captureBrowserDiagnostics(page);
  let publisherBrowser: Browser | undefined;
  let publisherConsole: string[] = [];

  try {
    publisherBrowser = await chromium.launch({
      args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream']
    });
    const publisherContext = await publisherBrowser.newContext({
      baseURL: process.env.TIDE_MEDIA_BASE_URL ?? 'http://tide:8080',
      permissions: ['camera', 'microphone']
    });
    const publisher = await publisherContext.newPage();
    publisherConsole = captureBrowserDiagnostics(publisher);

    await joinMediaTestRoom(publisher, roomName, 'synthetic-publisher');
    const synthetic = await publishSyntheticCameraAndAudio(publisher);

    // Sequential late join: publications already exist when the receiver
    // connects, so no TrackPublished event can be required for projection.
    await joinMediaTestRoom(page, roomName, `${testInfo.project.name}-receiver`);
    await expect(page.getByTestId('playback-blocked')).toHaveCount(0);
    await expectMediaInvariant(page);
    const persistentTags = await tagMediaElements(page);
    expect(Object.keys(persistentTags).sort()).toEqual(
      [synthetic.cameraSid, synthetic.microphoneSid].sort()
    );

    // Active-speaker storms and view changes only change CSS placement.
    await page.getByRole('button', { name: 'Speaker view' }).click();
    await page.evaluate(async () => {
      const room = (
        window as Window &
          typeof globalThis & {
            __tideRoom: {
              localParticipant: unknown;
              remoteParticipants: Map<string, unknown>;
              emit(event: string, participants: unknown[]): void;
            };
          }
      ).__tideRoom;
      const remote = [...room.remoteParticipants.values()][0];
      for (let index = 0; index < 30; index += 1) {
        room.emit('activeSpeakersChanged', index % 2 === 0 ? [remote] : []);
        await new Promise((resolve) => setTimeout(resolve, 35));
      }
      room.emit('activeSpeakersChanged', []);
    });
    await expectMediaElementTags(page, persistentTags);
    await page.getByRole('button', { name: 'Grid view' }).click();

    // Repeated screen publication must only mount/unmount the focus element.
    for (let cycle = 0; cycle < 2; cycle += 1) {
      const screen = await publishSyntheticScreen(publisher);
      await expect(page.getByTestId('focus-pane')).toBeVisible({ timeout: 10_000 });
      await expectMediaInvariant(page);
      await expectMediaElementTags(page, persistentTags);
      await unpublishSynthetic(
        publisher,
        [screen.screenSid, screen.audioSid].filter((sid): sid is string => Boolean(sid))
      );
      await expect(page.getByTestId('focus-pane')).toBeHidden({ timeout: 10_000 });
      await expectMediaInvariant(page);
      await expectMediaElementTags(page, persistentTags);
      if (cycle === 0) {
        // Give WebKit's receiver transport time to finish renegotiating before
        // the next synthetic capture starts. The lifecycle assertions above
        // still require the first publication to disappear promptly.
        await page.waitForTimeout(750);
      }
    }

    // Repair both halves of the attachment invariant: DOM srcObject and the
    // SDK's attachedElements bookkeeping.
    await page.evaluate(({ cameraSid, microphoneSid }) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __tideMediaTest: {
              clearSrcObject(sid: string): boolean;
              forgetLiveKitAttachment(sid: string): boolean;
              reconcile(): void;
            };
          }
      ).__tideMediaTest;
      if (!hook.clearSrcObject(cameraSid)) throw new Error('Camera element was not found.');
      if (!hook.forgetLiveKitAttachment(microphoneSid)) {
        throw new Error('Audio element was not registered in LiveKit.');
      }
      hook.reconcile();
    }, synthetic);
    await expectMediaInvariant(page);
    await expectMediaElementTags(page, persistentTags);

    // A transient autoplay rejection recovers on its own, so it must not put
    // a control in front of the user. Only a persistent block does — see the
    // next block.
    await page.evaluate((microphoneSid) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __tideMediaTest: {
              rejectNextPlayback(kind: 'audio'): void;
              clearSrcObject(sid: string): boolean;
              reconcile(): void;
            };
          }
      ).__tideMediaTest;
      hook.rejectNextPlayback('audio');
      hook.clearSrcObject(microphoneSid);
      hook.reconcile();
    }, synthetic.microphoneSid);
    await expect
      .poll(async () => (await probe(page)).playback, { timeout: 10_000 })
      .toMatchObject({ audio: true, video: true });
    await expect(page.getByTestId('playback-blocked')).toHaveCount(0);

    // Persistently blocked playback is surfaced rather than left as silence,
    // and the control clears once the browser allows it again.
    await page.evaluate((microphoneSid) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __tideMediaTest: {
              blockPlayback(kind: 'audio'): void;
              clearSrcObject(sid: string): boolean;
              reconcile(): void;
            };
          }
      ).__tideMediaTest;
      hook.blockPlayback('audio');
      hook.clearSrcObject(microphoneSid);
      hook.reconcile();
    }, synthetic.microphoneSid);
    await expect(page.getByTestId('playback-blocked')).toBeVisible({ timeout: 15_000 });

    // Blocked playback must be *quiet*. livekit-client reports playback status
    // from two probes that disagree while an attempt is running — the elements
    // stay blocked, the AudioContext resumes and reports healthy — so a client
    // that retries on either report spins at thousands of play() calls a
    // second and starves the page. Retrying cannot help: only a gesture can.
    const playCallsBefore = await playCalls(page);
    await page.waitForTimeout(3_000);
    expect(
      (await playCalls(page)) - playCallsBefore,
      'blocked playback must not drive a retry loop'
    ).toBeLessThan(10);

    await page.evaluate(() =>
      (
        window as Window & typeof globalThis & { __tideMediaTest: { unblockPlayback(): void } }
      ).__tideMediaTest.unblockPlayback()
    );
    await page.getByTestId('playback-blocked').click();
    await expect(page.getByTestId('playback-blocked')).toHaveCount(0, { timeout: 15_000 });
    await expectMediaInvariant(page);
    await expectMediaElementTags(page, persistentTags);

    // A recorded subscription failure is a display fact, not a state machine.
    // The reconcile tick re-derives it away because the publication is in
    // fact subscribed, and nothing about the media is disturbed meanwhile.
    await page.evaluate((cameraSid) => {
      (
        window as Window &
          typeof globalThis & {
            __tideMediaTest: { injectSubscriptionFailure(sid: string): void };
          }
      ).__tideMediaTest.injectSubscriptionFailure(cameraSid);
    }, synthetic.cameraSid);
    await expect
      .poll(async () => (await probe(page)).subscriptionFailures[synthetic.cameraSid], {
        timeout: 10_000
      })
      .toBeUndefined();
    await expect(page.getByTestId('playback-blocked')).toHaveCount(0);
    await expectMediaInvariant(page);

    // Full PC reconnect is authoritative and must converge without a reload or
    // changing any participant-owned media node.
    await page.evaluate(() =>
      (
        window as Window &
          typeof globalThis & {
            __tideRoom: { simulateScenario(scenario: 'full-reconnect'): Promise<void> };
          }
      ).__tideRoom.simulateScenario('full-reconnect')
    );
    await expect
      .poll(async () => (await probe(page)).connectionState, { timeout: 45_000 })
      .toBe('connected');
    await expectMediaInvariant(page);
    await expectMediaElementTags(page, persistentTags);

    // A subscription dropped behind tide's back — no event, no UI action — is
    // restored by applySubscriptions on the next tick.
    //
    // Deliberately last. Re-subscribing gets a *new* track from the SFU, so the
    // camera's video element is legitimately rebuilt, which would invalidate
    // the element-identity baseline every assertion above compares against.
    // (It only shows up on a slow enough runner: when the re-subscribe wins the
    // race the unsubscribed state is never projected and the element survives.)
    await page.evaluate((cameraSid) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __tideMediaTest: { unsubscribeBehindBack(sid: string): boolean };
          }
      ).__tideMediaTest;
      if (!hook.unsubscribeBehindBack(cameraSid)) {
        throw new Error('Camera publication was not found on any remote participant.');
      }
    }, synthetic.cameraSid);
    await expect
      .poll(
        async () => {
          const snapshot = await probe(page);
          const camera = snapshot.participants
            .flatMap((participant) => Object.values(participant.publications))
            .find((publication) => publication.publicationSid === synthetic.cameraSid);
          return camera?.subscribed === true && camera.desired;
        },
        { timeout: 20_000 }
      )
      .toBe(true);
    await expectMediaInvariant(page, {
      structureTimeout: resubscribeSettleTimeout,
      flowTimeout: resubscribeSettleTimeout
    });
  } finally {
    await attachDiagnostics(testInfo, 'receiver-console.json', receiverConsole);
    await attachDiagnostics(testInfo, 'publisher-console.json', publisherConsole);
    const finalProbe = await probe(page).catch((error) => ({ error: String(error) }));
    await attachDiagnostics(testInfo, 'rtc-final.json', finalProbe);
    await publisherBrowser?.close();
  }
});

/** Raw HTMLMediaElement.play() calls since page load — a spin detector. */
async function playCalls(page: Page): Promise<number> {
  return page.evaluate(
    () =>
      (
        window as Window & typeof globalThis & { __tideMediaTest: { playCalls(): number } }
      ).__tideMediaTest.playCalls() as number
  );
}
