import { MediaDeviceFailure } from 'livekit-client';

// What a local capture failure means to the person in the meeting. Pure: the
// browser facts it depends on are passed in, so the mapping is testable
// without a browser (e2e/device-error-messages.spec.ts).

/** The local medium a failure belongs to. */
export type MediaErrorKind = 'microphone' | 'camera' | 'screen' | 'speaker';

/** A device problem the meeting chrome shows, until it is fixed or dismissed. */
export interface MediaErrorView {
  kind: MediaErrorKind;
  message: string;
  /** When it happened relative to the others: higher is newer. */
  order: number;
}

/** Whether the failure came from turning a medium on or from switching its device. */
export type MediaAction = 'start' | 'switch';

export interface MediaEnvironment {
  /** `window.isSecureContext`: browsers only expose capture on https and localhost. */
  secureContext: boolean;
  /** Whether `navigator.mediaDevices` exists at all. */
  mediaDevices: boolean;
}

/** `room.switchActiveDevice` resolved false: the device in use is not the one picked. */
export class DeviceSwitchError extends Error {
  override readonly name = 'DeviceSwitchError';
}

export function currentMediaEnvironment(): MediaEnvironment {
  if (typeof window === 'undefined') return { secureContext: true, mediaDevices: true };
  return {
    secureContext: window.isSecureContext,
    mediaDevices: typeof navigator.mediaDevices !== 'undefined'
  };
}

type Failure =
  | 'denied'
  | 'dismissed'
  | 'blocked-by-system'
  | 'missing'
  | 'in-use'
  | 'unsupported'
  | 'not-sent'
  | 'other';

// livekit-client's own errors for a track that was captured but could not be
// published: the device worked, the connection did not.
const publishFailures = new Set([
  'ConnectionError',
  'NegotiationError',
  'PublishTrackError',
  'SignalReconnectError',
  'SignalRequestError',
  'UnexpectedConnectionState',
  'UnsupportedServer'
]);

function errorName(error: unknown): string {
  return error !== null && typeof error === 'object' && 'name' in error ? String(error.name) : '';
}

function errorText(error: unknown): string {
  if (error instanceof Error) return error.message;
  return typeof error === 'string' ? error : '';
}

/** livekit-client sometimes wraps the browser's error; the browser's is the one that says why. */
function rootError(error: unknown): unknown {
  const cause = error instanceof Error ? (error as Error & { cause?: unknown }).cause : undefined;
  return errorName(cause) ? cause : error;
}

function classify(error: unknown): Failure {
  // MediaDeviceFailure.getFailure uses `'name' in error`, which throws on a
  // primitive; a rejection with a string or undefined is just unexplained.
  if (error === null || typeof error !== 'object') return 'other';
  const name = errorName(error);
  // livekit-client files these under Other; for a person they mean the device is gone.
  if (name === 'OverconstrainedError' || name === 'ConstraintNotSatisfiedError') return 'missing';
  if (name === 'DeviceUnsupportedError') return 'unsupported';
  if (publishFailures.has(name)) return 'not-sent';
  switch (MediaDeviceFailure.getFailure(error)) {
    case MediaDeviceFailure.PermissionDenied:
      // Chrome says "Permission denied by system" when macOS refuses the
      // browser, and "Permission dismissed" when the prompt was closed.
      // Safari and Firefox word an OS-level denial like any other refusal,
      // so there it reads as blocked by the site, or for a screen share as a
      // cancel: a known limitation.
      if (/system/i.test(errorText(error))) return 'blocked-by-system';
      if (/dismiss/i.test(errorText(error))) return 'dismissed';
      return 'denied';
    case MediaDeviceFailure.NotFound:
      return 'missing';
    case MediaDeviceFailure.DeviceInUse:
      return 'in-use';
    default:
      return 'other';
  }
}

const devices = {
  microphone: { noun: 'microphone', Noun: 'Microphone', retry: 'unmute', Retry: 'Unmute' },
  camera: {
    noun: 'camera',
    Noun: 'Camera',
    retry: 'turn the camera on',
    Retry: 'Turn the camera on'
  }
} as const;

/**
 * What to tell the person, or undefined when nothing went wrong from their
 * side (a cancelled screen picker). Says what happened and what to do next.
 */
export function describeMediaError(
  kind: MediaErrorKind,
  error: unknown,
  environment: MediaEnvironment,
  action: MediaAction = 'start'
): string | undefined {
  const root = rootError(error);
  if (kind === 'screen') return describeScreenError(root, environment);
  if (kind === 'speaker') return "Couldn't switch the speaker. Choose another one.";

  const { noun, Noun, retry, Retry } = devices[kind];
  if (!environment.mediaDevices) {
    return environment.secureContext
      ? `This browser can't use a ${noun}. Open the meeting in another browser.`
      : `Your browser only allows the ${noun} on https pages. Open the meeting over https.`;
  }
  const subject = action === 'switch' ? `That ${noun}` : `Your ${noun}`;
  switch (classify(root)) {
    case 'denied':
      return `${Noun} access is blocked. Allow it in your browser's site settings, then ${retry}.`;
    case 'dismissed':
      return `The ${noun} prompt was closed. ${Retry} again and allow access.`;
    case 'blocked-by-system':
      return `Your computer is blocking the ${noun} for this browser. Allow it in system settings, then ${retry}.`;
    case 'missing':
      return `${subject} wasn't found. Check it's connected, or choose another one.`;
    case 'in-use':
      return action === 'switch'
        ? `${subject} is in use by another app. Close that app, or choose another one.`
        : `${subject} is in use by another app. Close that app, then ${retry}.`;
    case 'unsupported':
      return `This browser can't use a ${noun}. Open the meeting in another browser.`;
    case 'not-sent':
      return `Your ${noun} couldn't be sent to the meeting. Check your connection, then ${retry}.`;
    default:
      return action === 'switch'
        ? `Couldn't switch to that ${noun}. Choose another one.`
        : `${subject} didn't start. Try again, or choose another one.`;
  }
}

function describeScreenError(error: unknown, environment: MediaEnvironment): string | undefined {
  if (!environment.mediaDevices) {
    return environment.secureContext
      ? "This browser can't share its screen. Share from a desktop browser instead."
      : 'Your browser only allows screen sharing on https pages. Open the meeting over https.';
  }
  switch (classify(error)) {
    case 'denied':
    case 'dismissed':
      // Closing the picker without choosing. Chrome, Firefox and Safari all
      // report it as NotAllowedError, the same name they use for a site
      // blocked from sharing, so the two cannot be told apart, and cancelling
      // is far more common. Chrome's one distinguishable case, the operating
      // system refusing the browser ("Permission denied by system"), is
      // blocked-by-system below.
      return undefined;
    case 'blocked-by-system':
      return 'Your computer is blocking screen sharing for this browser. Allow it in system settings, then try again.';
    case 'unsupported':
      return "This browser can't share its screen. Share from a desktop browser instead.";
    case 'not-sent':
      return "Your screen couldn't be sent to the meeting. Check your connection, then try again.";
    default:
      return "Screen sharing didn't start. Try again, or share a different window.";
  }
}

/** A one-line account of the failure for the diagnostics ledger. */
export function mediaErrorDetail(kind: MediaErrorKind, error: unknown): string {
  const root = rootError(error);
  const name = errorName(root) || typeof root;
  const text = errorText(root);
  return text ? `${kind}: ${name}: ${text}` : `${kind}: ${name}`;
}

/** Whether the failure says the chosen device is gone, so the default one is worth trying. */
export function isMissingDevice(error: unknown): boolean {
  return classify(rootError(error)) === 'missing';
}
