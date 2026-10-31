import { expect, test, type Page } from '@playwright/test';
import { fakeSfu, fakeSfuLaunchArgs } from './fake-sfu';
import { defaultState, mockApi } from './mock-api';

// Pre-join remembers a guest's name, devices and on/off state once they have
// joined (issue #4). The API is mocked, the SFU is fake-sfu.ts, and the
// devices are synthetic; the spec swaps the device list through
// localStorage['spec:devices'] between visits, as plugging and unplugging.

test.use({ launchOptions: { args: fakeSfuLaunchArgs } });

interface FakeDevice {
  kind: MediaDeviceKind;
  deviceId: string;
  label: string;
}

const front: FakeDevice = { kind: 'videoinput', deviceId: 'cam-front', label: 'Front camera' };
const back: FakeDevice = { kind: 'videoinput', deviceId: 'cam-back', label: 'Back camera' };
const micA: FakeDevice = { kind: 'audioinput', deviceId: 'mic-a', label: 'Desk microphone' };
const micB: FakeDevice = { kind: 'audioinput', deviceId: 'mic-b', label: 'Headset microphone' };

async function installDevices(page: Page, devices: FakeDevice[]): Promise<void> {
  await page.addInitScript((initial) => {
    const list = () => {
      try {
        const swapped = localStorage.getItem('spec:devices');
        if (swapped) return JSON.parse(swapped) as FakeDevice[];
      } catch {
        // No storage: the initial list stands.
      }
      return initial;
    };

    MediaDevices.prototype.enumerateDevices = async function () {
      return list().map(
        (device) => ({ ...device, groupId: 'test', toJSON: () => ({}) }) as MediaDeviceInfo
      );
    };

    const idOf = (value: ConstrainDOMString | undefined) =>
      typeof value === 'string' ? value : undefined;

    MediaDevices.prototype.getUserMedia = async function (constraints) {
      const stream = new MediaStream();
      for (const kind of ['video', 'audio'] as const) {
        const constraint = constraints?.[kind];
        if (!constraint) continue;
        const asked = typeof constraint === 'object' ? constraint.deviceId : undefined;
        const exact =
          typeof asked === 'object' && !Array.isArray(asked) ? idOf(asked.exact) : idOf(asked);
        const ideal =
          typeof asked === 'object' && !Array.isArray(asked) ? idOf(asked.ideal) : undefined;
        const known = list().filter((device) => device.kind === `${kind}input`);
        if (exact && !known.some((device) => device.deviceId === exact)) {
          throw new DOMException('Requested device not found', 'OverconstrainedError');
        }
        const deviceId =
          [exact, ideal].find((id) => known.some((device) => device.deviceId === id)) ??
          known[0]?.deviceId ??
          '';
        let track: MediaStreamTrack;
        if (kind === 'video') {
          const canvas = document.createElement('canvas');
          canvas.width = 320;
          canvas.height = 240;
          canvas.getContext('2d')?.fillRect(0, 0, 320, 240);
          track = canvas.captureStream(15).getVideoTracks()[0];
        } else {
          const context = new AudioContext();
          const destination = context.createMediaStreamDestination();
          context.createOscillator().connect(destination);
          track = destination.stream.getAudioTracks()[0];
        }
        const settings = track.getSettings.bind(track);
        track.getSettings = () => ({ ...settings(), deviceId });
        stream.addTrack(track);
      }
      return stream;
    };
  }, devices);
}

const toggle = (page: Page, control: 'camera' | 'microphone') =>
  page.locator(`button[aria-pressed][aria-label*="${control}" i]`).first();

async function openPrejoin(page: Page, slug = 'standup'): Promise<void> {
  await page.goto(`/m/${slug}`);
  await expect(page.getByRole('button', { name: 'Join meeting' })).toBeEnabled();
}

async function join(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'Join meeting' }).click();
  await expect(page.getByRole('navigation', { name: 'Meeting controls' })).toBeVisible({
    timeout: 20_000
  });
}

function guestState() {
  const state = defaultState();
  state.signedIn = false;
  state.rooms = [
    { ...state.rooms[0], lobby_enabled: false },
    { ...state.rooms[0], id: 'r-other', slug: 'other-room', name: 'Other', lobby_enabled: false }
  ];
  return state;
}

async function setup(page: Page, devices: FakeDevice[]): Promise<void> {
  const state = guestState();
  await mockApi(page, state);
  await fakeSfu(page);
  await installDevices(page, devices);
}

const camera = (page: Page) => page.getByRole('combobox', { name: 'Camera' });
const microphone = (page: Page) => page.getByRole('combobox', { name: 'Microphone' });

test('a guest who has joined once finds name, devices and on/off state prefilled', async ({
  page
}) => {
  await setup(page, [front, back, micA, micB]);
  await openPrejoin(page);
  await expect(page.getByRole('textbox', { name: 'Name' })).toHaveValue('');
  await expect(camera(page)).toHaveValue('cam-front');

  await page.getByRole('textbox', { name: 'Name' }).fill('Grace Guest');
  await camera(page).selectOption('cam-back');
  await microphone(page).selectOption('mic-b');
  await toggle(page, 'camera').click();
  await expect(toggle(page, 'camera')).toHaveAttribute('aria-pressed', 'false');
  await join(page);

  await openPrejoin(page, 'other-room');
  await expect(page.getByRole('textbox', { name: 'Name' })).toHaveValue('Grace Guest');
  await expect(camera(page)).toHaveValue('cam-back');
  await expect(microphone(page)).toHaveValue('mic-b');
  await expect(toggle(page, 'camera')).toHaveAttribute('aria-pressed', 'false');
  await expect(toggle(page, 'microphone')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('choices are saved on Join only', async ({ page }) => {
  await setup(page, [front, back, micA, micB]);
  await openPrejoin(page);
  await page.getByRole('textbox', { name: 'Name' }).fill('Never Joined');
  await camera(page).selectOption('cam-back');

  await openPrejoin(page);
  await expect(page.getByRole('textbox', { name: 'Name' })).toHaveValue('');
  await expect(camera(page)).toHaveValue('cam-front');
});

test('a remembered device that is unplugged falls back to the default without an error', async ({
  page
}) => {
  await setup(page, [front, back, micA, micB]);
  await openPrejoin(page);
  await page.getByRole('textbox', { name: 'Name' }).fill('Grace Guest');
  await camera(page).selectOption('cam-back');
  await microphone(page).selectOption('mic-b');
  await join(page);

  await page.evaluate(
    (devices) => localStorage.setItem('spec:devices', devices),
    JSON.stringify([front, micA])
  );
  await openPrejoin(page);
  await expect(page.getByRole('textbox', { name: 'Name' })).toHaveValue('Grace Guest');
  await expect(camera(page)).toHaveValue('cam-front');
  await expect(microphone(page)).toHaveValue('mic-a');
  await expect(toggle(page, 'camera')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('a device whose id rotated is matched by its label', async ({ page }) => {
  await setup(page, [front, back, micA]);
  await openPrejoin(page);
  await page.getByRole('textbox', { name: 'Name' }).fill('Grace Guest');
  await camera(page).selectOption('cam-back');
  await join(page);

  const rotated = [{ ...front, deviceId: 'new-front' }, { ...back, deviceId: 'new-back' }, micA];
  await page.evaluate(
    (devices) => localStorage.setItem('spec:devices', devices),
    JSON.stringify(rotated)
  );
  await openPrejoin(page);
  await expect(camera(page)).toHaveValue('new-back');
});

test('a signed-in host keeps the session name; only devices and state are remembered', async ({
  page
}) => {
  await mockApi(page, defaultState());
  await fakeSfu(page);
  await installDevices(page, [front, back, micA]);
  await page.addInitScript(() =>
    localStorage.setItem('tide:prejoin:v1', JSON.stringify({ name: 'Grace Guest' }))
  );
  await openPrejoin(page);
  await expect(page.getByRole('textbox', { name: 'Name' })).toHaveValue('Ada Host');

  await camera(page).selectOption('cam-back');
  await join(page);

  const stored = await page.evaluate(() => localStorage.getItem('tide:prejoin:v1'));
  const saved = JSON.parse(stored ?? '{}');
  expect(saved.name).toBe('Grace Guest');
  expect(saved.camera).toEqual({ id: 'cam-back', label: 'Back camera' });
});
