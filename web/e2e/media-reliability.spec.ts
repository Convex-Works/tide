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
    await expect(page.getByText('Media paused', { exact: true })).toHaveCount(0);
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

    // A transient autoplay rejection is retried without exposing recovery UI.
    // Persistent browser policy failures are armed for the next ordinary user
    // gesture by RoomState's capture listener.
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
    await expect
      .poll(async () => (await probe(page)).playback, { timeout: 10_000 })
      .toMatchObject({ audio: true, video: true });
    await expect(page.getByText('Media paused', { exact: true })).toHaveCount(0);

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

    // A later failure is also retried in the background; recovery no longer
    // depends on a dedicated banner button.
    await page.evaluate((cameraSid) => {
      (
        window as Window &
          typeof globalThis & {
            __klisiMediaTest: {
              injectSubscriptionFailure(sid: string): void;
            };
          }
      ).__klisiMediaTest.injectSubscriptionFailure(cameraSid);
    }, synthetic.cameraSid);
    await expect
      .poll(async () => (await probe(page)).subscriptionRetryTotals[synthetic.cameraSid], {
        timeout: 10_000
      })
      .toBe(retriesBefore + 2);
    await expect(page.getByText('Media paused', { exact: true })).toHaveCount(0);
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
