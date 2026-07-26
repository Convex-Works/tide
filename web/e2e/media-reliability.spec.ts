import { chromium, expect, test, type Browser } from '@playwright/test';
import {
  attachDiagnostics,
  captureBrowserDiagnostics,
  expectMediaElementTags,
  expectMediaInvariant,
  joinMediaTestRoom,
  probe,
  publishSyntheticCameraAndAudio,
  publishSyntheticScreen,
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
      baseURL: process.env.KLISI_MEDIA_BASE_URL ?? 'http://klisi:8080',
      permissions: ['camera', 'microphone']
    });
    const publisher = await publisherContext.newPage();
    publisherConsole = captureBrowserDiagnostics(publisher);

    await joinMediaTestRoom(publisher, roomName, 'synthetic-publisher');
    const synthetic = await publishSyntheticCameraAndAudio(publisher);

    // Sequential late join: publications already exist when the receiver
    // connects, so no TrackPublished event can be required for projection.
    await joinMediaTestRoom(page, roomName, `${testInfo.project.name}-receiver`);
    const recovery = page.getByTestId('media-recovery');
    if (await recovery.isVisible().catch(() => false)) {
      await recovery.getByRole('button', { name: 'Resume' }).click();
      await expect(recovery).toBeHidden({ timeout: 10_000 });
    }
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
            __klisiRoom: {
              localParticipant: unknown;
              remoteParticipants: Map<string, unknown>;
              emit(event: string, participants: unknown[]): void;
            };
          }
      ).__klisiRoom;
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
    }

    // Repair both halves of the attachment invariant: DOM srcObject and the
    // SDK's attachedElements bookkeeping.
    await page.evaluate(({ cameraSid, microphoneSid }) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __klisiMediaTest: {
              clearSrcObject(sid: string): boolean;
              forgetLiveKitAttachment(sid: string): boolean;
              reconcile(): void;
            };
          }
      ).__klisiMediaTest;
      if (!hook.clearSrcObject(cameraSid)) throw new Error('Camera element was not found.');
      if (!hook.forgetLiveKitAttachment(microphoneSid)) {
        throw new Error('Audio element was not registered in LiveKit.');
      }
      hook.reconcile();
    }, synthetic);
    await expectMediaInvariant(page);
    await expectMediaElementTags(page, persistentTags);

    // A deliberate autoplay rejection must remain visible until the recovery
    // click calls Room.startAudio in the user-activation stack.
    await page.evaluate((microphoneSid) => {
      const hook = (
        window as Window &
          typeof globalThis & {
            __klisiMediaTest: {
              rejectNextPlayback(kind: 'audio'): void;
              clearSrcObject(sid: string): boolean;
              reconcile(): void;
            };
          }
      ).__klisiMediaTest;
      hook.rejectNextPlayback('audio');
      hook.clearSrcObject(microphoneSid);
      hook.reconcile();
    }, synthetic.microphoneSid);
    await expect(recovery).toBeVisible({ timeout: 5_000 });
    await recovery.getByRole('button', { name: 'Resume' }).click();
    await expect(recovery).toBeHidden({ timeout: 10_000 });
    await expect
      .poll(async () => (await probe(page)).playback, { timeout: 10_000 })
      .toMatchObject({ audio: true, video: true });

    // A synthetic failure receives one automatic resubscribe cycle.
    const retriesBefore = (await probe(page)).subscriptionRetryTotals[synthetic.cameraSid] ?? 0;
    await page.evaluate((cameraSid) => {
      (
        window as Window &
          typeof globalThis & {
            __klisiMediaTest: { injectSubscriptionFailure(sid: string): void };
          }
      ).__klisiMediaTest.injectSubscriptionFailure(cameraSid);
    }, synthetic.cameraSid);
    await expect
      .poll(async () => (await probe(page)).subscriptionRetryTotals[synthetic.cameraSid] ?? 0, {
        timeout: 10_000
      })
      .toBe(retriesBefore + 1);
    await page.waitForTimeout(750);
    expect((await probe(page)).subscriptionRetryTotals[synthetic.cameraSid]).toBe(
      retriesBefore + 1
    );
    await expectMediaInvariant(page);

    // If that bounded retry also fails, the exact SID becomes an observable
    // recoverable error. Resume is a user-driven retry and must clear it.
    await page.evaluate((cameraSid) => {
      (
        window as Window &
          typeof globalThis & {
            __klisiMediaTest: {
              injectSubscriptionFailure(sid: string, exhausted?: boolean): void;
            };
          }
      ).__klisiMediaTest.injectSubscriptionFailure(cameraSid, true);
    }, synthetic.cameraSid);
    await expect
      .poll(async () => (await probe(page)).subscriptionFailures[synthetic.cameraSid], {
        timeout: 5_000
      })
      .toMatchObject({
        publicationSid: synthetic.cameraSid,
        recoverable: true,
        retrying: false,
        exhausted: true,
        attempts: 1
      });
    await page.waitForTimeout(500);
    expect((await probe(page)).subscriptionRetryTotals[synthetic.cameraSid]).toBe(
      retriesBefore + 1
    );
    await expect(recovery).toBeVisible();
    await recovery.getByRole('button', { name: 'Resume' }).click();
    await expect
      .poll(async () => (await probe(page)).subscriptionRetryTotals[synthetic.cameraSid], {
        timeout: 10_000
      })
      .toBe(retriesBefore + 2);
    await expect(recovery).toBeHidden({ timeout: 10_000 });
    await expectMediaInvariant(page);

    // Full PC reconnect is authoritative and must converge without a reload or
    // changing any participant-owned media node.
    await page.evaluate(() =>
      (
        window as Window &
          typeof globalThis & {
            __klisiRoom: { simulateScenario(scenario: 'full-reconnect'): Promise<void> };
          }
      ).__klisiRoom.simulateScenario('full-reconnect')
    );
    await expect
      .poll(async () => (await probe(page)).connectionState, { timeout: 45_000 })
      .toBe('connected');
    await expectMediaInvariant(page);
    await expectMediaElementTags(page, persistentTags);
  } finally {
    await attachDiagnostics(testInfo, 'receiver-console.json', receiverConsole);
    await attachDiagnostics(testInfo, 'publisher-console.json', publisherConsole);
    const finalProbe = await probe(page).catch((error) => ({ error: String(error) }));
    await attachDiagnostics(testInfo, 'rtc-final.json', finalProbe);
    await publisherBrowser?.close();
  }
});
