import { expect, test } from '@playwright/test';
import {
  DeviceSwitchError,
  describeMediaError,
  isMissingDevice,
  mediaErrorDetail,
  type MediaEnvironment
} from '../src/lib/rtc/mediaErrors';

// The error-to-message mapping on its own, in Node: no page, no browser.
// device-errors.spec.ts covers it reaching the meeting chrome.

const https: MediaEnvironment = { secureContext: true, mediaDevices: true };

/** What a browser rejects with: a DOMException-shaped error with that name. */
function browserError(name: string, message = ''): Error {
  const error = new Error(message);
  error.name = name;
  return error;
}

test('camera and microphone failures say what happened and what to do', () => {
  const cases: [Parameters<typeof describeMediaError>[0], Error, string][] = [
    [
      'camera',
      browserError('NotReadableError', 'Could not start video source'),
      'Your camera is in use by another app. Close that app, then turn the camera on.'
    ],
    [
      'microphone',
      browserError('NotReadableError'),
      'Your microphone is in use by another app. Close that app, then unmute.'
    ],
    [
      'camera',
      browserError('NotAllowedError', 'Permission denied'),
      "Camera access is blocked. Allow it in your browser's site settings, then turn the camera on."
    ],
    [
      'microphone',
      browserError('NotAllowedError', 'Permission denied'),
      "Microphone access is blocked. Allow it in your browser's site settings, then unmute."
    ],
    [
      'camera',
      browserError('NotAllowedError', 'Permission denied by system'),
      'Your computer is blocking the camera for this browser. Allow it in system settings, then turn the camera on.'
    ],
    [
      'camera',
      browserError('NotFoundError', 'Requested device not found'),
      "Your camera wasn't found. Check it's connected, or choose another one."
    ],
    // livekit-client files this under Other; for a person it is the same as not found.
    [
      'microphone',
      browserError('OverconstrainedError'),
      "Your microphone wasn't found. Check it's connected, or choose another one."
    ],
    [
      'camera',
      browserError('AbortError', 'Starting videoinput failed'),
      "Your camera didn't start. Try again, or choose another one."
    ]
  ];
  for (const [kind, error, message] of cases) {
    expect(describeMediaError(kind, error, https), `${kind} ${error.name}`).toBe(message);
  }
});

test('a failed device switch names the device picked, not the one in use', () => {
  expect(describeMediaError('camera', browserError('NotReadableError'), https, 'switch')).toBe(
    'That camera is in use by another app. Close that app, or choose another one.'
  );
  expect(describeMediaError('microphone', browserError('NotFoundError'), https, 'switch')).toBe(
    "That microphone wasn't found. Check it's connected, or choose another one."
  );
  expect(
    describeMediaError('camera', new DeviceSwitchError('not the one chosen'), https, 'switch')
  ).toBe("Couldn't switch to that camera. Choose another one.");
  expect(describeMediaError('speaker', new Error('cannot switch audio output'), https)).toBe(
    "Couldn't switch the speaker. Choose another one."
  );
});

test('capture without https is explained, whatever the error says', () => {
  // Without a secure context navigator.mediaDevices is undefined, and the
  // SDK fails with a TypeError reading getUserMedia off it.
  const missing = new TypeError("Cannot read properties of undefined (reading 'getUserMedia')");
  const http: MediaEnvironment = { secureContext: false, mediaDevices: false };
  expect(describeMediaError('camera', missing, http)).toBe(
    'Your browser only allows the camera on https pages. Open the meeting over https.'
  );
  expect(describeMediaError('microphone', missing, http)).toBe(
    'Your browser only allows the microphone on https pages. Open the meeting over https.'
  );
  expect(describeMediaError('screen', missing, http)).toBe(
    'Your browser only allows screen sharing on https pages. Open the meeting over https.'
  );
  expect(describeMediaError('camera', missing, { secureContext: true, mediaDevices: false })).toBe(
    "This browser can't use a camera."
  );
});

test('a cancelled screen picker is not an error; other screen failures are', () => {
  // Chrome, Firefox and Safari all reject a cancelled picker with NotAllowedError.
  for (const message of [
    'Permission denied',
    'The request is not allowed by the user agent or the platform in the current context.'
  ]) {
    expect(describeMediaError('screen', browserError('NotAllowedError', message), https)).toBe(
      undefined
    );
  }
  expect(
    describeMediaError(
      'screen',
      browserError('NotAllowedError', 'Permission denied by system'),
      https
    )
  ).toBe(
    'Your computer is blocking screen sharing for this browser. Allow it in system settings, then try again.'
  );
  expect(describeMediaError('screen', browserError('NotReadableError'), https)).toBe(
    "Screen sharing didn't start. Try again, or share a different window."
  );
  // livekit-client's error when the browser has no getDisplayMedia (phones).
  expect(describeMediaError('screen', browserError('DeviceUnsupportedError'), https)).toBe(
    "This browser can't share its screen."
  );
});

test('a wrapped browser error is read through its cause', () => {
  const wrapped = new Error('publishing failed', {
    cause: browserError('NotReadableError', 'Device in use')
  });
  expect(describeMediaError('camera', wrapped, https)).toBe(
    'Your camera is in use by another app. Close that app, then turn the camera on.'
  );
  expect(mediaErrorDetail('camera', wrapped)).toBe('camera: NotReadableError: Device in use');
});

test('only a missing device is worth retrying on the default one', () => {
  expect(isMissingDevice(browserError('NotFoundError'))).toBe(true);
  expect(isMissingDevice(browserError('OverconstrainedError'))).toBe(true);
  expect(isMissingDevice(browserError('NotReadableError'))).toBe(false);
  expect(isMissingDevice(browserError('NotAllowedError'))).toBe(false);
});
