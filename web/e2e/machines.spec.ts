import { expect, test, type Page } from '@playwright/test';
import {
  AuthLoginPath,
  MachinePath,
  PairingConfirmPath,
  PairingDenyPath,
  PairingPath,
  RecordingTranscriptPath,
  RoomRecordingsPath,
  type PairingInfo
} from '../src/lib/api/types.gen';
import { bundle, machine, mockApi, now, recording, wireNull } from './mock-api';

// The /machines page and the recordings' transcript cells (ARCHITECTURE.md
// §8.1), against the Vite dev server with the API mocked in the browser.

const code = 'WDJB-MJHT';
const pairingInfo: PairingInfo = {
  code,
  name: 'Ada’s MacBook Pro',
  os: 'macos',
  arch: 'aarch64',
  app_version: '0.4.2',
  expires_at: now + 600,
  moil_url: 'https://klisi.example.com/moil'
};

function at(template: string, value: string): string {
  return template.replace(/\{[a-z]+\}/, encodeURIComponent(value));
}

test.describe('pairing a machine', () => {
  test('confirming the code pairs the machine and lists it', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;

    await page.goto(`/machines?code=${code}`);
    await expect(page.getByRole('heading', { name: 'Pair this machine?' })).toBeVisible();
    await expect(page.getByTestId('pairing-code')).toHaveText(code);
    await expect(page.getByText('Ada’s MacBook Pro')).toBeVisible();
    await expect(page.getByText('macOS · aarch64')).toBeVisible();
    await expect(page.getByText('0.4.2')).toBeVisible();

    await page.getByRole('button', { name: 'Pair', exact: true }).click();

    await expect(page.getByRole('status')).toContainText('Paired Ada’s MacBook Pro');
    const confirm = api.calls.filter((call) => call.path === at(PairingConfirmPath, code));
    expect(confirm).toHaveLength(1);
    expect(confirm[0]).toMatchObject({ method: 'POST', csrf: '1' });
    expect(api.count('POST', at(PairingDenyPath, code))).toBe(0);

    // The code is spent, so it leaves the address; the list shows the machine.
    await expect(page).toHaveURL(/\/machines$/);
    const row = page.locator('[data-machine-id="m-wdjb-mjht"]');
    await expect(row).toContainText('Ada’s MacBook Pro');
    await expect(row.getByTestId('approve-hint')).toBeVisible();
  });

  test('denying the code turns the machine away', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;

    await page.goto(`/machines?code=${code}`);
    await page.getByRole('button', { name: 'Deny' }).click();

    await expect(page.getByRole('heading', { name: 'Machine not paired' })).toBeVisible();
    await expect(page.getByText('Ada’s MacBook Pro was turned away')).toBeVisible();
    const deny = api.calls.filter((call) => call.path === at(PairingDenyPath, code));
    expect(deny).toHaveLength(1);
    expect(deny[0]).toMatchObject({ method: 'POST', csrf: '1' });
    expect(api.count('POST', at(PairingConfirmPath, code))).toBe(0);
    expect(api.state.machines.machines).toHaveLength(0);

    await page.getByRole('link', { name: 'Your machines' }).click();
    await expect(page.getByRole('heading', { name: 'Machines', exact: true })).toBeVisible();
    await expect(page.getByText('No machines yet.')).toBeVisible();
  });

  test('an unknown or expired code ends with a way to start again', async ({ page }) => {
    const api = await mockApi(page);

    await page.goto('/machines?code=ZZZZ-ZZZZ');
    await expect(page.getByRole('heading', { name: 'This code no longer works' })).toBeVisible();
    await expect(page.getByText('Start pairing again in the moil app.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Pair', exact: true })).toHaveCount(0);
    expect(api.count('GET', at(PairingPath, 'ZZZZ-ZZZZ'))).toBe(1);
  });

  test('a code that expires while the card is open ends the same way', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;

    await page.goto(`/machines?code=${code}`);
    await expect(page.getByTestId('pairing-code')).toHaveText(code);
    delete api.state.pairings[code];
    await page.getByRole('button', { name: 'Pair', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'This code no longer works' })).toBeVisible();
  });

  test('signed out, sign-in returns to the same code', async ({ page }) => {
    const api = await mockApi(page);
    api.state.signedIn = false;

    await page.goto(`/machines?code=${code}`);
    await expect(page.getByRole('heading', { name: 'Sign in to pair this machine' })).toBeVisible();
    const href = await page.getByRole('link', { name: 'Sign in' }).getAttribute('href');
    const target = new URL(href ?? '', 'http://localhost');
    expect(target.pathname).toBe(AuthLoginPath);
    expect(target.searchParams.get('next')).toBe(`/machines?code=${code}`);
  });
});

test.describe('your machines', () => {
  test('lists state, flags an unapproved bundle and links the moil app', async ({ page }) => {
    const api = await mockApi(page);
    api.state.machines.machines = [
      machine({ id: 'm-online', name: 'Studio Mac mini', state: 'idle' }),
      machine({ id: 'm-busy', name: 'Workstation', os: 'linux', arch: 'x86_64', state: 'busy' }),
      machine({ id: 'm-paused', name: 'Travel laptop', state: 'paused' }),
      machine({
        id: 'm-offline',
        name: 'Old iMac',
        state: 'offline',
        last_seen_at: now - 7_200,
        approved: false
      })
    ];

    await page.clock.setFixedTime(now * 1000);
    await page.goto('/machines');

    const state = (id: string) =>
      page.locator(`[data-machine-id="${id}"]`).getByTestId('machine-state');
    await expect(state('m-online')).toHaveText('Online');
    await expect(state('m-busy')).toHaveText('Busy');
    await expect(state('m-paused')).toHaveText('Paused');
    await expect(state('m-offline')).toHaveText('Offline · last seen 2h ago');
    await expect(page.locator('[data-machine-id="m-busy"]')).toContainText('Linux · x86_64');

    // Only the machine that hasn't approved this bundle asks for it, naming
    // it the way the moil app does: version and a 12-character hash prefix.
    await expect(page.getByTestId('approve-hint')).toHaveCount(1);
    const hint = page.locator('[data-machine-id="m-offline"]').getByTestId('approve-hint');
    await expect(hint).toContainText(`${bundle.name} ${bundle.version}`);
    await expect(hint).toContainText('a00a65c16629');
    await expect(hint).not.toContainText(bundle.hash);

    await expect(page.getByRole('link', { name: 'Add a machine' })).toHaveAttribute(
      'href',
      'moil://pair?url=https%3A%2F%2Fklisi.example.com%2Fmoil'
    );
    await expect(page.getByLabel('Or paste this address into the moil app')).toHaveValue(
      'https://klisi.example.com/moil'
    );
  });

  test('copies the moil address', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await mockApi(page);

    await page.goto('/machines');
    await page.getByRole('button', { name: 'Copy address' }).click();
    await expect(page.getByRole('button', { name: 'Address copied' })).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      'https://klisi.example.com/moil'
    );
  });

  test('unpairs on the second click only', async ({ page }) => {
    const api = await mockApi(page);
    api.state.machines.machines = [
      machine({ id: 'm-keep', name: 'Keeper' }),
      machine({ id: 'm-drop', name: 'Leaver', state: 'offline', last_seen_at: wireNull })
    ];

    await page.goto('/machines');
    await expect(page.locator('[data-machine-id="m-drop"]')).toContainText('Never connected');

    await page.getByRole('button', { name: 'Unpair Leaver' }).click();
    expect(api.count('DELETE', at(MachinePath, 'm-drop'))).toBe(0);
    await page.getByRole('button', { name: 'Confirm unpair Leaver' }).click();

    await expect(page.locator('[data-machine-id="m-drop"]')).toHaveCount(0);
    await expect(page.locator('[data-machine-id="m-keep"]')).toBeVisible();
    const remove = api.calls.filter((call) => call.path === at(MachinePath, 'm-drop'));
    expect(remove).toHaveLength(1);
    expect(remove[0]).toMatchObject({ method: 'DELETE', csrf: '1' });
  });

  test('signed out, sign-in returns here', async ({ page }) => {
    const api = await mockApi(page);
    api.state.signedIn = false;

    await page.goto('/machines');
    await expect(page.getByRole('heading', { name: 'Sign in to see your machines' })).toBeVisible();
    const href = await page.getByRole('link', { name: 'Sign in' }).getAttribute('href');
    expect(new URL(href ?? '', 'http://localhost').searchParams.get('next')).toBe('/machines');
  });

  test('the dashboard header leads here', async ({ page }) => {
    await mockApi(page);
    await page.goto('/');
    await page.getByRole('link', { name: 'Machines' }).click();
    await expect(page).toHaveURL(/\/machines$/);
    await expect(page.getByRole('heading', { name: 'Machines', exact: true })).toBeVisible();
  });
});

test.describe('transcripts in the recordings list', () => {
  const recordingsPath = at(RoomRecordingsPath, 'standup');
  const row = (page: Page, id: string) => page.locator(`[data-recording-id="${id}"]`);
  const cell = (page: Page, id: string) => row(page, id).getByTestId('transcript');

  test('renders every transcript status', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [
      recording('none', wireNull),
      recording('available', { status: 'available' }),
      recording('waiting', { status: 'waiting', message: 'No machine is online.' }),
      recording('running', { status: 'running', progress: 0.42, message: 'Finding speakers' }),
      recording('completed', { status: 'completed', speakers: 3 }),
      recording('failed', { status: 'failed', error: 'The machine ran out of memory.' })
    ];
    // Keep the in-flight rows from polling while the test looks.
    await page.clock.install({ time: now * 1000 });

    await page.goto('/rooms/standup');
    await expect(row(page, 'none')).toBeVisible();

    await expect(cell(page, 'none')).toBeHidden();
    await expect(cell(page, 'available').getByRole('button', { name: 'Transcribe' })).toBeVisible();

    await expect(cell(page, 'waiting')).toContainText('waiting');
    await expect(cell(page, 'waiting').getByTitle('No machine is online.').first()).toBeVisible();

    await expect(cell(page, 'running')).toContainText('transcribing');
    await expect(cell(page, 'running')).toContainText('42%');
    await expect(cell(page, 'running')).toContainText('Finding speakers');

    await expect(cell(page, 'completed')).toContainText('3 speakers');
    await expect(cell(page, 'completed').getByRole('link', { name: 'Transcript' })).toHaveAttribute(
      'href',
      `${at(RecordingTranscriptPath, 'completed')}/download?format=txt`
    );
    await expect(cell(page, 'completed').getByRole('link', { name: 'Captions' })).toHaveAttribute(
      'href',
      `${at(RecordingTranscriptPath, 'completed')}/download?format=vtt`
    );

    await expect(cell(page, 'failed')).toContainText('failed');
    await expect(
      cell(page, 'failed').getByTitle('The machine ran out of memory.').first()
    ).toBeVisible();
    await expect(cell(page, 'failed').getByRole('button', { name: 'Retry' })).toBeVisible();
    expect(api.calls.filter((call) => call.method !== 'GET')).toHaveLength(0);
  });

  test('Transcribe requests a transcript and the row follows it', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('rec-1', { status: 'available' })];
    api.state.requested = { status: 'waiting', message: 'No machine is online.' };

    await page.goto('/rooms/standup');
    await cell(page, 'rec-1').getByRole('button', { name: 'Transcribe' }).click();

    await expect(cell(page, 'rec-1')).toHaveAttribute('data-status', 'waiting');
    await expect(cell(page, 'rec-1')).toContainText('No machine is online.');
    const posts = api.calls.filter((call) => call.path === at(RecordingTranscriptPath, 'rec-1'));
    expect(posts).toHaveLength(1);
    expect(posts[0]).toMatchObject({ method: 'POST', csrf: '1' });
  });

  test('Retry requests a failed transcript again', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [
      recording('rec-2', { status: 'failed', error: 'The machine ran out of memory.' })
    ];
    api.state.requested = { status: 'running', progress: 0, message: 'Starting' };

    await page.clock.install({ time: now * 1000 });
    await page.goto('/rooms/standup');
    await cell(page, 'rec-2').getByRole('button', { name: 'Retry' }).click();

    await expect(cell(page, 'rec-2')).toHaveAttribute('data-status', 'running');
    await expect(cell(page, 'rec-2')).toContainText('0%');
    const posts = api.calls.filter((call) => call.path === at(RecordingTranscriptPath, 'rec-2'));
    expect(posts).toHaveLength(1);
    expect(posts[0]).toMatchObject({ method: 'POST', csrf: '1' });
  });

  test('polls fast while running, slowly while waiting, and stops after', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [
      recording('rec-3', { status: 'running', progress: 0.1, message: 'Transcribing' })
    ];
    const loads = () => api.count('GET', recordingsPath);

    await page.clock.install({ time: now * 1000 });
    await page.goto('/rooms/standup');
    await expect(cell(page, 'rec-3')).toContainText('10%');
    // Freeze time: from here on only runFor fires the page's timers.
    await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 100);

    // Advances the page's clock, then lets any request a timer started land
    // (fetches resolve in real time) before counting list loads.
    async function after(ms: number): Promise<number> {
      await page.clock.runFor(ms);
      await page.waitForTimeout(250);
      return loads();
    }

    // Running: the list comes back after ~3 s and the row follows it. (The
    // first timer started while the page loaded in real time, hence the slack.)
    const first = loads();
    api.state.recordings[0].transcript = { status: 'running', progress: 0.6 };
    expect(await after(1_000)).toBe(first);
    expect(await after(2_100)).toBe(first + 1);
    await expect(cell(page, 'rec-3')).toContainText('60%');

    // Waiting: the fast poll stops; the slow one comes after ~15 s.
    api.state.recordings[0].transcript = { status: 'waiting', message: 'Every machine is busy.' };
    expect(await after(3_100)).toBe(first + 2);
    await expect(cell(page, 'rec-3')).toContainText('Every machine is busy.');
    expect(await after(3_100)).toBe(first + 2);
    expect(await after(8_000)).toBe(first + 2);
    api.state.recordings[0].transcript = { status: 'completed', speakers: 2 };
    expect(await after(4_000)).toBe(first + 3);
    await expect(cell(page, 'rec-3')).toContainText('2 speakers');

    // Nothing in flight: the list stops polling.
    expect(await after(3_100)).toBe(first + 3);
    expect(await after(15_100)).toBe(first + 3);
    expect(await after(120_000)).toBe(first + 3);
  });
});
