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
  fail: {
    video?: string;
    audio?: string;
    videoMessage?: string;
    display?: string;
    displayMessage?: string;
  };
  /** Device ids that are "unplugged": asking for one exactly is OverconstrainedError. */
  missing: string[];
  /**
   * Requests for these device ids (or 'video' for any camera) wait, the way
   * a permission prompt or a slow device does, until __tideCaptureRelease.
   */
  hold: string[];
  /** Milliseconds every camera request takes before it answers. */
  videoDelayMs: number;
  /** Every getUserMedia call's constraints, in order. */
  requests: MediaStreamConstraints[];
  displayCalls: number;
}

type CaptureWindow = Window & {
  __tideCapture: CaptureControl;
  /** Lets held requests go on, or rejects them with a DOMException of that name. */
  __tideCaptureRelease: (errorName?: string) => void;
};

/** Synthetic devices and capture, so every failure is the one the spec asked for. */
async function installCapture(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const control: CaptureControl = {
      fail: {},
      missing: [],
      hold: [],
      videoDelayMs: 0,
      requests: [],
      displayCalls: 0
    };
    const captureWindow = window as unknown as CaptureWindow;
    captureWindow.__tideCapture = control;
    let held: { resolve: () => void; reject: (error: Error) => void }[] = [];
    captureWindow.__tideCaptureRelease = (errorName) => {
      const waiting = held;
      held = [];
      for (const request of waiting) {
        if (errorName) request.reject(new DOMException('Permission denied', errorName));
        else request.resolve();
      }
    };

    const deviceIdOf = (constraint: boolean | MediaTrackConstraints | undefined) => {
      if (!constraint || typeof constraint !== 'object')
        return { exact: undefined, any: undefined };
      const deviceId = constraint.deviceId;
      if (typeof deviceId === 'string') return { exact: undefined, any: deviceId };
      if (deviceId && typeof deviceId === 'object' && !Array.isArray(deviceId)) {
        const exact = typeof deviceId.exact === 'string' ? deviceId.exact : undefined;
        const ideal = typeof deviceId.ideal === 'string' ? deviceId.ideal : undefined;
        return { exact, any: exact ?? ideal };
      }
      return { exact: undefined, any: undefined };
    };

    // LiveKit compares the id it asked for with getSettings().deviceId; a
    // canvas or Web Audio track has none, so report the one requested.
    const withDeviceId = (track: MediaStreamTrack, deviceId: string): MediaStreamTrack => {
      const settings = track.getSettings.bind(track);
      track.getSettings = () => ({ ...settings(), deviceId });
      return track;
    };

    const videoTrack = (deviceId: string): MediaStreamTrack => {
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
      return withDeviceId(canvas.captureStream(15).getVideoTracks()[0], deviceId);
    };

    const audioTrack = (deviceId: string): MediaStreamTrack => {
      const context = new AudioContext();
      const oscillator = context.createOscillator();
      const destination = context.createMediaStreamDestination();
      oscillator.connect(destination);
      oscillator.start();
      return withDeviceId(destination.stream.getAudioTracks()[0], deviceId);
    };

    MediaDevices.prototype.enumerateDevices = async function () {
      const device = (kind: MediaDeviceKind, deviceId: string, label: string) =>
        ({ kind, deviceId, label, groupId: 'test', toJSON: () => ({}) }) as MediaDeviceInfo;
      return [
        device('videoinput', 'cam-1', 'Test camera 1'),
        device('videoinput', 'cam-2', 'Test camera 2'),
        device('videoinput', 'cam-3', 'Test camera 3'),
        device('audioinput', 'mic-1', 'Test microphone')
      ];
    };

    MediaDevices.prototype.getUserMedia = async function (constraints) {
      control.requests.push(JSON.parse(JSON.stringify(constraints ?? {})));
      const video = constraints?.video;
      const audio = constraints?.audio;
      const videoId = deviceIdOf(video);
      const audioId = deviceIdOf(audio);
      if (video && control.videoDelayMs > 0) {
        await new Promise((resolve) => setTimeout(resolve, control.videoDelayMs));
      }
      if (
        video &&
        (control.hold.includes('video') ||
          (videoId.any !== undefined && control.hold.includes(videoId.any)))
      ) {
        await new Promise<void>((resolve, reject) => held.push({ resolve, reject }));
      }
      if (video && control.fail.video) {
        throw new DOMException(
          control.fail.videoMessage ?? 'Could not start video source',
          control.fail.video
        );
      }
      if (audio && control.fail.audio) {
        throw new DOMException('Could not start audio source', control.fail.audio);
      }
      for (const wanted of [videoId.exact, audioId.exact]) {
        if (wanted && control.missing.includes(wanted)) {
          throw new DOMException('Requested device not found', 'OverconstrainedError');
        }
      }
      const stream = new MediaStream();
      if (video) stream.addTrack(videoTrack(videoId.any ?? 'cam-1'));
      if (audio) stream.addTrack(audioTrack(audioId.any ?? 'mic-1'));
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

function releaseCapture(page: Page, errorName?: string): Promise<void> {
  return page.evaluate(
    (name) => (window as unknown as CaptureWindow).__tideCaptureRelease(name),
    errorName
  );
}

function captureRequests(page: Page): Promise<MediaStreamConstraints[]> {
  return page.evaluate(() => (window as unknown as CaptureWindow).__tideCapture.requests);
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

type RoomWindow = Window & {
  __tideRoom?: {
    localParticipant: {
      trackPublications: Map<
        string,
        {
          source: string;
          isMuted: boolean;
          track?: { mediaStreamTrack: MediaStreamTrack };
        }
      >;
    };
  };
};

function localSources(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    [...((window as RoomWindow).__tideRoom?.localParticipant.trackPublications.values() ?? [])]
      .filter((publication) => !publication.isMuted)
      .map((publication) => publication.source)
      .sort()
  );
}

/** The device the published camera track is really capturing from. */
function cameraDeviceId(page: Page): Promise<string | undefined> {
  return page.evaluate(() => {
    const camera = [
      ...((window as RoomWindow).__tideRoom?.localParticipant.trackPublications.values() ?? [])
    ].find((publication) => publication.source === 'camera');
    const track = camera?.track?.mediaStreamTrack;
    return track && track.readyState === 'live' ? track.getSettings().deviceId : undefined;
  });
}

const cameraInUse =
  'Your camera is in use by another app. Close that app, then turn the camera on.';
const micInUse = 'Your microphone is in use by another app. Close that app, then unmute.';
const cameraBlocked =
  "Camera access is blocked. Allow it in your browser's site settings, then turn the camera on.";

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

test("one medium's fallback at join does not hide another medium's failure", async ({ page }) => {
  // The microphone is in use and fails at once. The chosen camera is gone,
  // and LiveKit only answers that a moment later: its MediaDevicesError
  // arrives after the microphone's, then the default camera works.
  await enter(page, { camera: true, microphone: true }, () =>
    setCapture(page, { fail: { audio: 'NotReadableError' }, missing: ['cam-1'], videoDelayMs: 300 })
  );
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  await expect.poll(() => localSources(page), { timeout: 10_000 }).toEqual(['camera']);
  await expect(bar.getByRole('button', { name: 'Turn camera off' })).toHaveAttribute(
    'aria-pressed',
    'true'
  );
  await expect(bar.getByRole('button', { name: 'Unmute microphone' })).toHaveAttribute(
    'aria-pressed',
    'false'
  );
  await expect(bar.getByRole('alert')).toHaveText(micInUse);
});

test('two failures at once: dismissing the one shown reveals the other', async ({ page }) => {
  await enter(page, { camera: true, microphone: true }, () =>
    setCapture(page, { fail: { audio: 'NotReadableError', video: 'NotAllowedError' } })
  );
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  const alert = bar.getByRole('alert');
  await expect(alert).toHaveText(/^(Your microphone is in use|Camera access is blocked)/);
  const first = await alert.textContent();
  await bar.getByRole('button', { name: 'Dismiss' }).click();
  await expect(alert).toHaveText(first === micInUse ? cameraBlocked : micInUse);
  await bar.getByRole('button', { name: 'Dismiss' }).click();
  await expect(alert).toHaveCount(0);
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
  await expect(bar.getByRole('alert')).toHaveText(cameraBlocked);
  await expect(bar.getByRole('button', { name: 'Turn camera on' })).toHaveAttribute(
    'aria-pressed',
    'false'
  );
  await bar.getByRole('button', { name: 'Dismiss' }).click();
  await expect(bar.getByRole('alert')).toHaveCount(0);
});

test('a second click while the camera prompt is up does not clear the refusal later', async ({
  page
}) => {
  // LiveKit makes a second setCameraEnabled(true) during the first wait on
  // the first's publication for up to 10 s, then resolve with nothing.
  test.slow();
  await enter(page, { camera: false, microphone: false });
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  const camera = bar.getByRole('button', { name: 'Turn camera on' });

  await setCapture(page, { hold: ['video'], requests: [] });
  await camera.click();
  await expect.poll(async () => (await captureRequests(page)).length).toBe(1);
  // Impatient: the button still says "Turn camera on".
  await camera.click();

  await releaseCapture(page, 'NotAllowedError');
  await expect(bar.getByRole('alert')).toHaveText(cameraBlocked);
  // Past LiveKit's 10 s pending-publication wait: whatever the second click
  // started has settled by now, and the refusal must still be shown. The
  // wait is the bug's own timer; there is no event to wait on instead.
  await page.waitForTimeout(11_000);
  await expect(bar.getByRole('alert')).toHaveText(cameraBlocked);
  await expect(camera).toHaveAttribute('aria-pressed', 'false');
  await expect(camera).toHaveAttribute('aria-busy', 'false');
  // The second click joined the first rather than asking again.
  expect((await captureRequests(page)).length).toBe(1);
});

test('a camera turned off while it is still starting ends up off', async ({ page }) => {
  await enter(page, { camera: true, microphone: false });
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  await bar.getByRole('button', { name: 'Turn camera off' }).click();
  await expect(bar.getByRole('button', { name: 'Turn camera on' })).toHaveAttribute(
    'aria-pressed',
    'false'
  );

  // Turning it back on re-opens the device, which takes a while; the user
  // changes their mind as soon as the button says it is on.
  await setCapture(page, { hold: ['video'] });
  await bar.getByRole('button', { name: 'Turn camera on' }).click();
  await releaseCapture(page);
  await bar.getByRole('button', { name: 'Turn camera off' }).click();
  await expect(bar.getByRole('button', { name: 'Turn camera on' })).toHaveAttribute(
    'aria-busy',
    'false'
  );
  await expect(bar.getByRole('button', { name: 'Turn camera on' })).toHaveAttribute(
    'aria-pressed',
    'false'
  );
  await expect.poll(() => localSources(page)).toEqual([]);
});

test('a slow failed camera switch does not undo the switch made after it', async ({ page }) => {
  await enter(page, { camera: true, microphone: false });
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  await expect.poll(() => cameraDeviceId(page), { timeout: 10_000 }).toBe('cam-1');

  const pick = async (label: string) => {
    await bar.getByRole('button', { name: 'Choose camera' }).click();
    await bar.getByRole('menuitemradio', { name: label }).click();
  };
  // Camera 2 hangs and then turns out to be in use; meanwhile the user
  // picks camera 3, which works. LiveKit runs the two device restarts one
  // after the other, so camera 3 opens once camera 2 has failed.
  await setCapture(page, { hold: ['cam-2'], requests: [] });
  await pick('Test camera 2');
  await expect
    .poll(async () => JSON.stringify(await captureRequests(page)))
    .toContain('"exact":"cam-2"');
  await setCapture(page, { hold: [] });
  await pick('Test camera 3');
  await releaseCapture(page, 'NotReadableError');

  await expect.poll(() => cameraDeviceId(page), { timeout: 10_000 }).toBe('cam-3');
  // A revert would queue behind camera 3's restart and run straight after
  // it; give it ample time to show, then camera 3 must still be the one.
  await page.waitForTimeout(1_000);
  expect(await cameraDeviceId(page)).toBe('cam-3');
  const opened = (await captureRequests(page)).map((request) =>
    JSON.stringify((request.video as MediaTrackConstraints | undefined)?.deviceId)
  );
  expect(opened.slice(opened.indexOf('{"exact":"cam-3"}') + 1)).toEqual([]);
  // The failure belonged to a choice the user has already replaced.
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
  // The share request has fully settled, error handling included, once the
  // control is no longer busy; only then is the quiet meaningful.
  await expect(share).toHaveAttribute('aria-busy', 'false');
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
  const videoIds = (await captureRequests(page))
    .filter((request) => request.video)
    .map((request) => (request.video as MediaTrackConstraints).deviceId);
  expect(videoIds[0]).toEqual({ exact: 'cam-1' });
  expect(videoIds.at(-1)).not.toEqual({ exact: 'cam-1' });
});
