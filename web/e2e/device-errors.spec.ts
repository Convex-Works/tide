import { expect, test, type Page } from '@playwright/test';
import { defaultState, mockApi } from './mock-api';
import { fakeSfu, fakeSfuLaunchArgs } from './fake-sfu';

// A camera or microphone that fails must not cost the meeting: the user
// stays in with that medium off and the control bar says why. The API is
// mocked, the SFU is fake-sfu.ts, and capture is synthetic, steered from the
// spec through window.__tideCapture.

test.use({ launchOptions: { args: fakeSfuLaunchArgs } });

interface CaptureControl {
  /** The DOMException name the next captures of that kind reject with, until cleared. */
  fail: { video?: string; audio?: string; display?: string; displayMessage?: string };
  /** Device ids that are "unplugged": asking for one exactly is OverconstrainedError. */
  missing: string[];
  /** Every getUserMedia call's constraints, in order. */
  requests: MediaStreamConstraints[];
  displayCalls: number;
}

type CaptureWindow = Window & { __tideCapture: CaptureControl };

/** Synthetic devices and capture, so every failure is the one the spec asked for. */
async function installCapture(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const control: CaptureControl = { fail: {}, missing: [], requests: [], displayCalls: 0 };
    (window as unknown as CaptureWindow).__tideCapture = control;

    const exactDeviceId = (constraint: boolean | MediaTrackConstraints | undefined) => {
      if (!constraint || typeof constraint !== 'object') return undefined;
      const deviceId = constraint.deviceId;
      if (deviceId && typeof deviceId === 'object' && !Array.isArray(deviceId)) {
        return typeof deviceId.exact === 'string' ? deviceId.exact : undefined;
      }
      return undefined;
    };

    const videoTrack = (): MediaStreamTrack => {
      const canvas = document.createElement('canvas');
      canvas.width = 320;
      canvas.height = 240;
      const context = canvas.getContext('2d');
      let hue = 0;
      setInterval(() => {
        hue = (hue + 7) % 360;
        if (!context) return;
        context.fillStyle = `hsl(${hue} 60% 50%)`;
        context.fillRect(0, 0, canvas.width, canvas.height);
      }, 66);
      return canvas.captureStream(15).getVideoTracks()[0];
    };

    const audioTrack = (): MediaStreamTrack => {
      const context = new AudioContext();
      const oscillator = context.createOscillator();
      const destination = context.createMediaStreamDestination();
      oscillator.connect(destination);
      oscillator.start();
      return destination.stream.getAudioTracks()[0];
    };

    MediaDevices.prototype.enumerateDevices = async function () {
      const device = (kind: MediaDeviceKind, deviceId: string, label: string) =>
        ({ kind, deviceId, label, groupId: 'test', toJSON: () => ({}) }) as MediaDeviceInfo;
      return [
        device('videoinput', 'cam-1', 'Test camera'),
        device('audioinput', 'mic-1', 'Test microphone')
      ];
    };

    MediaDevices.prototype.getUserMedia = async function (constraints) {
      control.requests.push(JSON.parse(JSON.stringify(constraints ?? {})));
      const video = constraints?.video;
      const audio = constraints?.audio;
      if (video && control.fail.video) {
        throw new DOMException('Could not start video source', control.fail.video);
      }
      if (audio && control.fail.audio) {
        throw new DOMException('Could not start audio source', control.fail.audio);
      }
      for (const wanted of [exactDeviceId(video), exactDeviceId(audio)]) {
        if (wanted && control.missing.includes(wanted)) {
          throw new DOMException('Requested device not found', 'OverconstrainedError');
        }
      }
      const stream = new MediaStream();
      if (video) stream.addTrack(videoTrack());
      if (audio) stream.addTrack(audioTrack());
      return stream;
    };

    MediaDevices.prototype.getDisplayMedia = async function () {
      control.displayCalls += 1;
      throw new DOMException(
        control.fail.displayMessage ?? 'Permission denied',
        control.fail.display ?? 'NotAllowedError'
      );
    };
  });
}

function setCapture(page: Page, change: Partial<CaptureControl>): Promise<void> {
  return page.evaluate((next) => {
    Object.assign((window as unknown as CaptureWindow).__tideCapture, next);
  }, change);
}

async function enter(
  page: Page,
  media: { camera: boolean; microphone: boolean },
  beforeJoin?: () => Promise<void>
): Promise<void> {
  await mockApi(page, defaultState());
  await fakeSfu(page);
  await installCapture(page);
  await page.goto('/m/standup');
  for (const [control, wanted] of [
    ['camera', media.camera],
    ['microphone', media.microphone]
  ] as const) {
    const toggle = page.locator(`button[aria-pressed][aria-label*="${control}" i]`).first();
    await expect(toggle).toBeVisible();
    // The preview has opened (or failed to open) the device before it is toggled.
    await expect(toggle).toHaveAttribute('aria-pressed', 'true');
    if (!wanted) await toggle.click();
    await expect(toggle).toHaveAttribute('aria-pressed', String(wanted));
  }
  await beforeJoin?.();
  await page.getByRole('button', { name: 'Join meeting' }).click();
  await expect(page.getByRole('button', { name: 'Share screen' })).toBeVisible({ timeout: 20_000 });
}

function localSources(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const room = (
      window as Window & {
        __tideRoom?: {
          localParticipant: {
            trackPublications: Map<string, { source: string; isMuted: boolean }>;
          };
        };
      }
    ).__tideRoom;
    return [...(room?.localParticipant.trackPublications.values() ?? [])]
      .filter((publication) => !publication.isMuted)
      .map((publication) => publication.source)
      .sort();
  });
}

const cameraInUse =
  'Your camera is in use by another app. Close that app, then turn the camera on.';

test('a camera in use at join leaves the user in the meeting with the microphone on', async ({
  page
}) => {
  // The preview opens the camera fine; it is taken by another app between
  // pre-join and join.
  await enter(page, { camera: true, microphone: true }, () =>
    setCapture(page, { fail: { video: 'NotReadableError' } })
  );

  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  await expect(bar.getByRole('alert')).toHaveText(cameraInUse);
  await expect(bar.getByRole('button', { name: 'Turn camera on' })).toHaveAttribute(
    'aria-pressed',
    'false'
  );
  await expect(bar.getByRole('button', { name: 'Mute microphone' })).toHaveAttribute(
    'aria-pressed',
    'true'
  );
  await expect.poll(() => localSources(page), { timeout: 10_000 }).toEqual(['microphone']);
  // Still in the meeting, not back at pre-join with a connection error.
  await expect(page.getByRole('button', { name: 'Join meeting' })).toHaveCount(0);
});

test('a camera that fails to turn on says why, and the message clears once it works', async ({
  page
}) => {
  await enter(page, { camera: false, microphone: false });
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  await expect(bar.getByRole('alert')).toHaveCount(0);

  await setCapture(page, { fail: { video: 'NotReadableError' } });
  await bar.getByRole('button', { name: 'Turn camera on' }).click();
  await expect(bar.getByRole('alert')).toHaveText(cameraInUse);
  await expect(bar.getByRole('button', { name: 'Turn camera on' })).toHaveAttribute(
    'aria-pressed',
    'false'
  );

  await setCapture(page, { fail: {} });
  await bar.getByRole('button', { name: 'Turn camera on' }).click();
  await expect(bar.getByRole('button', { name: 'Turn camera off' })).toHaveAttribute(
    'aria-pressed',
    'true'
  );
  await expect(bar.getByRole('alert')).toHaveCount(0);
  await expect.poll(() => localSources(page), { timeout: 10_000 }).toEqual(['camera']);

  // Turning an already published camera back on re-opens the device, a
  // different path in LiveKit; a refusal there is reported the same way and
  // can be dismissed.
  await bar.getByRole('button', { name: 'Turn camera off' }).click();
  await setCapture(page, { fail: { video: 'NotAllowedError' } });
  await bar.getByRole('button', { name: 'Turn camera on' }).click();
  await expect(bar.getByRole('alert')).toHaveText(
    "Camera access is blocked. Allow it in your browser's site settings, then turn the camera on."
  );
  await expect(bar.getByRole('button', { name: 'Turn camera on' })).toHaveAttribute(
    'aria-pressed',
    'false'
  );
  await bar.getByRole('button', { name: 'Dismiss' }).click();
  await expect(bar.getByRole('alert')).toHaveCount(0);
});

test('cancelling the screen picker shows nothing; a real failure does', async ({ page }) => {
  await enter(page, { camera: false, microphone: false });
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  const share = bar.getByRole('button', { name: 'Share screen' });
  const displayCalls = () =>
    page.evaluate(() => (window as unknown as CaptureWindow).__tideCapture.displayCalls);

  // Chrome, Firefox and Safari all reject a cancelled picker with NotAllowedError.
  await share.click();
  await expect.poll(displayCalls).toBe(1);
  // The rejection is immediate; give the UI the same time it takes to show
  // the failures below, and it must still be quiet.
  await page.waitForTimeout(500);
  await expect(bar.getByRole('alert')).toHaveCount(0);
  await expect(share).toHaveAttribute('aria-pressed', 'false');

  await setCapture(page, {
    fail: { display: 'NotReadableError', displayMessage: 'Could not start' }
  });
  await share.click();
  await expect(bar.getByRole('alert')).toHaveText(
    "Screen sharing didn't start. Try again, or share a different window."
  );

  // Chrome's one NotAllowedError that is not a cancel: macOS refusing the browser.
  await setCapture(page, {
    fail: { display: 'NotAllowedError', displayMessage: 'Permission denied by system' }
  });
  await share.click();
  await expect(bar.getByRole('alert')).toHaveText(
    'Your computer is blocking screen sharing for this browser. Allow it in system settings, then try again.'
  );
  await expect(share).toHaveAttribute('aria-pressed', 'false');
});

test('a camera unplugged after pre-join falls back to the default camera', async ({ page }) => {
  await enter(page, { camera: true, microphone: false }, () =>
    setCapture(page, { missing: ['cam-1'], requests: [] })
  );
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  await expect(bar.getByRole('button', { name: 'Turn camera off' })).toHaveAttribute(
    'aria-pressed',
    'true'
  );
  await expect.poll(() => localSources(page), { timeout: 10_000 }).toEqual(['camera']);
  await expect(bar.getByRole('alert')).toHaveCount(0);

  // It asked for the chosen camera exactly first, then for any camera.
  const requests = await page.evaluate(
    () => (window as unknown as CaptureWindow).__tideCapture.requests
  );
  const videoIds = requests
    .filter((request) => request.video)
    .map((request) => (request.video as MediaTrackConstraints).deviceId);
  expect(videoIds[0]).toEqual({ exact: 'cam-1' });
  expect(videoIds.at(-1)).not.toEqual({ exact: 'cam-1' });
});
