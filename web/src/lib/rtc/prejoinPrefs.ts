const key = 'tide:prejoin:v1';

export interface RememberedDevice {
  id: string;
  label: string;
}

export interface PrejoinPrefs {
  name?: string;
  camera?: RememberedDevice;
  microphone?: RememberedDevice;
  camEnabled?: boolean;
  micEnabled?: boolean;
}

function device(value: unknown): RememberedDevice | undefined {
  if (!value || typeof value !== 'object') return undefined;
  const { id, label } = value as Record<string, unknown>;
  if (typeof id !== 'string' || typeof label !== 'string') return undefined;
  return { id, label };
}

export function readPrejoinPrefs(): PrejoinPrefs {
  try {
    const raw = JSON.parse(localStorage.getItem(key) ?? 'null');
    if (!raw || typeof raw !== 'object') return {};
    return {
      name: typeof raw.name === 'string' ? raw.name : undefined,
      camera: device(raw.camera),
      microphone: device(raw.microphone),
      camEnabled: typeof raw.camEnabled === 'boolean' ? raw.camEnabled : undefined,
      micEnabled: typeof raw.micEnabled === 'boolean' ? raw.micEnabled : undefined
    };
  } catch {
    return {};
  }
}

export function writePrejoinPrefs(prefs: PrejoinPrefs): void {
  try {
    localStorage.setItem(key, JSON.stringify(prefs));
  } catch {
    // Storage unavailable or full: the choices just aren't remembered.
  }
}

export function rememberDevice(
  devices: MediaDeviceInfo[],
  deviceId: string
): RememberedDevice | undefined {
  const found = devices.find((candidate) => candidate.deviceId === deviceId);
  return found ? { id: found.deviceId, label: found.label } : undefined;
}

/** Browsers can rotate deviceIds between sessions, so the label is the second chance. */
export function pickDevice(
  devices: MediaDeviceInfo[],
  remembered: RememberedDevice | undefined
): string {
  const byId = remembered && devices.find((candidate) => candidate.deviceId === remembered.id);
  const byLabel =
    remembered?.label && devices.find((candidate) => candidate.label === remembered.label);
  return (byId || byLabel || devices[0])?.deviceId ?? '';
}
