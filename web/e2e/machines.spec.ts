import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  AuthLoginPath,
  MachineBusy,
  MachineIdle,
  MachineOffline,
  MachinePaused,
  MachinePath,
  MachinesPath,
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

// The server's messages (server/internal/transcripts/status.go).
const offlineMessage = 'Waiting for a paired machine to come online.';
const setupError =
  "The machine couldn't set up the transcriber. Check its disk space and network, then try again.";

function at(template: string, value: string): string {
  return template.replace(/\{[a-z]+\}/, encodeURIComponent(value));
}

/** A gate a routed request waits at until the spec opens it. */
function gate(): { wait: Promise<void>; open: () => void } {
  let open!: () => void;
  const wait = new Promise<void>((resolve) => (open = resolve));
  return { wait, open };
}

/**
 * Answers the next `times` requests to `path` with an error (status 0: the
 * network fails), then lets the mock answer again.
 */
async function failNext(
  page: Page,
  path: string,
  { times = 1, status = 500, error = 'Something went wrong. Try again.', method = 'GET' } = {}
): Promise<void> {
  let left = times;
  await page.route(
    (url) => url.pathname === path,
    async (route) => {
      if (left === 0 || route.request().method() !== method) return route.fallback();
      left -= 1;
      if (status === 0) return route.abort('internetdisconnected');
      await route.fulfill({
        status,
        contentType: 'application/json',
        body: JSON.stringify({ error })
      });
    }
  );
}

/** Pretends the tab was hidden or shown, as a browser does on switching tabs. */
async function setHidden(page: Page, hidden: boolean): Promise<void> {
  await page.evaluate((value) => {
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => value });
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => (value ? 'hidden' : 'visible')
    });
    document.dispatchEvent(new Event('visibilitychange'));
  }, hidden);
}

/**
 * The visible buttons and links inside each of `containers` that poke out of
 * its box, or whose own content is cut off.
 */
async function clipped(containers: Locator): Promise<string[]> {
  return containers.evaluateAll((elements) =>
    elements.flatMap((container) => {
      const box = container.getBoundingClientRect();
      return [...container.querySelectorAll<HTMLElement>('a, button')]
        .filter((control) => control.offsetParent !== null)
        .filter((control) => {
          const inner = control.getBoundingClientRect();
          return (
            inner.left < box.left - 0.5 ||
            inner.right > box.right + 0.5 ||
            control.scrollWidth > control.clientWidth + 0.5
          );
        })
        .map((control) => control.getAttribute('aria-label') ?? control.textContent?.trim() ?? '');
    })
  );
}

async function pageOverflows(page: Page): Promise<boolean> {
  return page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
}

test.describe('pairing a machine', () => {
  test('confirming the code pairs the machine and lists it', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;

    await page.goto(`/machines?code=${code}`);
    await expect(page.getByRole('heading', { name: 'Pair this machine?' })).toBeVisible();
    // The check that matters leads, in one sentence: the code alone proves
    // nothing, and the address must be klisi's.
    await expect(page.getByTestId('moil-address-check')).toHaveText(
      'Pair only if you just started pairing in the moil app on your own computer, and it shows this code and says it is pairing with https://klisi.example.com/moil. Otherwise, choose Deny.'
    );
    await expect(page.getByText('choose Deny')).toHaveCount(1);
    await expect(page.getByTestId('pairing-code')).toHaveText(code);
    await expect(page.locator('bdi', { hasText: 'Ada’s MacBook Pro' })).toBeVisible();
    await expect(page.getByText('macOS · aarch64')).toBeVisible();
    await expect(page.getByText('Reported by the machine')).toBeVisible();
    await expect(
      page.getByText(
        'Once paired, it downloads and transcribes every new recording of rooms you own, until you unpair it here.'
      )
    ).toBeVisible();
    await expect(page.getByText('Pairs with your account host@klisi.dev.')).toBeVisible();

    await page.getByRole('button', { name: 'Pair', exact: true }).click();

    // The banner takes focus, so a screen reader reads it out.
    const banner = page.getByRole('status').filter({ hasText: 'Paired' });
    await expect(banner).toContainText('Paired Ada’s MacBook Pro.');
    await expect(banner).toContainText('review and approve the transcribe bundle in its moil app');
    await expect(banner).toBeFocused();
    const confirm = api.calls.filter((call) => call.path === at(PairingConfirmPath, code));
    expect(confirm).toHaveLength(1);
    expect(confirm[0]).toMatchObject({ method: 'POST', csrf: '1' });
    expect(api.count('POST', at(PairingDenyPath, code))).toBe(0);

    // The code is spent, so it leaves the address; the list shows the machine
    // as the server reports one that has yet to connect.
    await expect(page).toHaveURL(/\/machines$/);
    const row = page.locator('[data-machine-id="m-wdjb-mjht"]');
    await expect(row).toContainText('Ada’s MacBook Pro');
    await expect(row.getByTestId('machine-state')).toHaveText('Never connected');
    await expect(row.getByTestId('approve-hint')).toBeVisible();
  });

  test('denying the code turns the machine away', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;

    await page.goto(`/machines?code=${code}`);
    await page.getByRole('button', { name: 'Deny' }).click();

    await expect(page.getByRole('heading', { name: 'Machine not paired' })).toBeFocused();
    await expect(
      page.getByText(
        'You denied Ada’s MacBook Pro. Nothing was shared with it, and its code no longer works.'
      )
    ).toBeVisible();
    const deny = api.calls.filter((call) => call.path === at(PairingDenyPath, code));
    expect(deny).toHaveLength(1);
    expect(deny[0]).toMatchObject({ method: 'POST', csrf: '1' });
    expect(api.count('POST', at(PairingConfirmPath, code))).toBe(0);
    // Like Pair, Deny spends the code, so it leaves the address.
    await expect(page).toHaveURL(/\/machines$/);

    await page.getByRole('button', { name: 'Your machines' }).click();
    await expect(page.getByRole('heading', { name: 'Machines', exact: true })).toBeFocused();
    await expect(page.getByTestId('onboarding')).toBeVisible();

    await page.reload();
    await expect(page.getByRole('heading', { name: 'Machines', exact: true })).toBeVisible();
  });

  test('a host without machines is shown how to start, until they have one', async ({ page }) => {
    const api = await mockApi(page);
    const { app_url, moil_url } = api.state.machines;

    await page.goto('/machines');
    const onboarding = page.getByTestId('onboarding');
    await expect(onboarding.getByRole('listitem')).toHaveCount(3);
    // 1: where to get the app, the latest release moil names, in a new tab.
    const download = onboarding.getByRole('link', { name: 'Download moil' });
    await expect(download).toHaveAttribute('href', app_url);
    await expect(download).toHaveAttribute('target', '_blank');
    await expect(page.getByRole('link', { name: 'Get moil' })).toHaveCount(0);
    // 2: the pairing it refers to is the deep link, and the address to paste.
    await expect(page.getByRole('link', { name: 'Add a machine' })).toHaveAttribute(
      'href',
      `moil://pair?url=${encodeURIComponent(moil_url)}`
    );
    await expect(page.getByLabel('Paste this address into its moil app')).toHaveValue(moil_url);
    // 3: which bundle to approve, by the hash prefix the app shows.
    const approve = onboarding.getByRole('listitem').nth(2);
    await expect(approve).toContainText(`${bundle.name} bundle, version ${bundle.version}`);
    await expect(approve).toContainText(`(${bundle.hash.slice(0, 12)})`);
    await expect(approve).not.toContainText(bundle.hash);

    // With a machine paired, the steps give way to the list, and the app
    // is still a link away.
    api.state.machines.machines = [machine({ id: 'm-first', name: 'Studio' })];
    await page.reload();
    await expect(page.getByText('Studio')).toBeVisible();
    await expect(page.getByTestId('onboarding')).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'Get moil' })).toHaveAttribute('href', app_url);
  });

  test('a code that does not work can be entered again', async ({ page }) => {
    const api = await mockApi(page);

    await page.goto('/machines?code=ZZZZ-ZZZZ');
    await expect(page.getByRole('heading', { name: "This code doesn't work" })).toBeVisible();
    await expect(page.getByText('It expired, was already used, or was mistyped.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Pair', exact: true })).toHaveCount(0);
    expect(api.count('GET', at(PairingPath, 'ZZZZ-ZZZZ'))).toBe(1);

    // The machine shows its real code; typed in any case, without the hyphen.
    api.state.pairings[code] = pairingInfo;
    await page.getByLabel('Pairing code').fill('wdjbmjht');
    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page).toHaveURL(/\/machines\?code=wdjbmjht$/);
    await expect(page.getByRole('heading', { name: 'Pair this machine?' })).toBeFocused();
    await expect(page.getByTestId('pairing-code')).toHaveText(code);
  });

  test('a code that expires while the card is open ends the same way', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;

    await page.goto(`/machines?code=${code}`);
    await expect(page.getByTestId('pairing-code')).toHaveText(code);
    delete api.state.pairings[code];
    await page.getByRole('button', { name: 'Pair', exact: true }).click();
    await expect(page.getByRole('heading', { name: "This code doesn't work" })).toBeFocused();
  });

  test('leaving while Pair is in flight stays where the host went', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;
    const held = gate();
    await page.route(
      (url) => url.pathname === at(PairingConfirmPath, code),
      async (route) => {
        await held.wait;
        await route.fallback();
      }
    );

    await page.goto(`/machines?code=${code}`);
    await page.getByRole('button', { name: 'Pair', exact: true }).click();
    await page.getByRole('link', { name: 'All rooms' }).click();
    await expect(page.getByRole('heading', { name: 'Rooms' })).toBeVisible();

    const confirmed = page.waitForResponse((response) =>
      response.url().endsWith(at(PairingConfirmPath, code))
    );
    held.open();
    expect((await confirmed).status()).toBe(201);
    // Whatever the finished Pair would do next has had time to happen.
    await page.waitForTimeout(500);
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(page.getByRole('heading', { name: 'Rooms' })).toBeVisible();
    expect(api.count('GET', MachinesPath)).toBe(0);
  });

  test('a network failure on Pair says what to do next', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;
    await failNext(page, at(PairingConfirmPath, code), { status: 0, method: 'POST' });

    await page.goto(`/machines?code=${code}`);
    await page.getByRole('button', { name: 'Pair', exact: true }).click();
    await expect(page.getByRole('alert')).toHaveText(
      'Could not pair the machine. Check your connection, then try again.'
    );

    // The card stays, and Pair works once the network is back.
    await page.getByRole('button', { name: 'Pair', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: 'Paired' })).toBeVisible();
  });

  test('paired, but the list failed: says both and loads it again', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;
    await failNext(page, MachinesPath, { error: 'Could not load your machines. Try again.' });

    await page.goto(`/machines?code=${code}`);
    await page.getByRole('button', { name: 'Pair', exact: true }).click();

    await expect(page.getByText('Paired Ada’s MacBook Pro.')).toBeVisible();
    await expect(page.getByRole('alert')).toHaveText('Could not load your machines. Try again.');
    await expect(page.getByRole('button', { name: 'Try again' })).toBeFocused();

    await page.getByRole('button', { name: 'Try again' }).click();
    await expect(page.getByRole('status').filter({ hasText: 'Paired' })).toContainText(
      'Paired Ada’s MacBook Pro.'
    );
    await expect(page.locator('[data-machine-id="m-wdjb-mjht"]')).toBeVisible();
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
      machine({ id: 'm-online', name: 'Studio Mac mini', state: MachineIdle }),
      machine({
        id: 'm-busy',
        name: 'Workstation',
        os: 'linux',
        arch: 'x86_64',
        state: MachineBusy
      }),
      machine({ id: 'm-paused', name: 'Travel laptop', state: MachinePaused }),
      machine({
        id: 'm-offline',
        name: 'Old iMac',
        state: MachineOffline,
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
    await expect(state('m-paused')).toHaveText('Paused in moil');
    await expect(state('m-offline')).toHaveText('Offline · last seen 2h ago');
    // The list names the system; its architecture is for the pairing card.
    await expect(page.locator('[data-machine-id="m-busy"]')).toContainText('Linux');
    await expect(page.locator('[data-machine-id="m-busy"]')).not.toContainText('x86_64');

    // Only the machine that hasn't approved this bundle asks for it, naming
    // it the way the moil app does: version and a 12-character hash prefix.
    await expect(page.getByTestId('approve-hint')).toHaveCount(1);
    const hint = page.locator('[data-machine-id="m-offline"]').getByTestId('approve-hint');
    await expect(hint).toContainText(
      `review and approve the ${bundle.name} bundle, version ${bundle.version} (a00a65c16629).`
    );
    await expect(hint).not.toContainText(bundle.hash);

    await expect(page.getByRole('link', { name: 'Add a machine' })).toHaveAttribute(
      'href',
      'moil://pair?url=https%3A%2F%2Fklisi.example.com%2Fmoil'
    );
    await expect(page.getByRole('heading', { name: 'Pair another computer' })).toBeVisible();
    // The app is linked where the server says moil's latest release is.
    await expect(page.getByRole('link', { name: 'Get moil' })).toHaveAttribute(
      'href',
      api.state.machines.app_url
    );
    await expect(page.getByLabel('Paste this address into its moil app')).toHaveValue(
      'https://klisi.example.com/moil'
    );
  });

  test('a pairing code typed here opens its card', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;

    await page.goto('/machines');
    await page.getByLabel('Enter the code it shows').fill(' wdjb-mjht ');
    await page.getByLabel('Enter the code it shows').press('Enter');

    await expect(page).toHaveURL(/\/machines\?code=wdjb-mjht$/);
    await expect(page.getByRole('heading', { name: 'Pair this machine?' })).toBeFocused();
    await expect(page.getByTestId('pairing-code')).toHaveText(code);
    await page.getByRole('button', { name: 'Pair', exact: true }).click();
    await expect(page.locator('[data-machine-id="m-wdjb-mjht"]')).toBeVisible();
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
      machine({ id: 'm-drop', name: 'Leaver', state: MachineOffline, last_seen_at: wireNull })
    ];

    await page.goto('/machines');
    await expect(page.locator('[data-machine-id="m-drop"]')).toContainText('Never connected');

    await page.getByRole('button', { name: 'Unpair Leaver' }).click();
    expect(api.count('DELETE', at(MachinePath, 'm-drop'))).toBe(0);
    await page.getByRole('button', { name: 'Confirm unpair Leaver' }).click();

    await expect(page.locator('[data-machine-id="m-drop"]')).toHaveCount(0);
    await expect(page.locator('[data-machine-id="m-keep"]')).toBeVisible();
    // The button that had focus is gone: focus goes to the list, and the
    // change is read out.
    await expect(page.getByRole('heading', { name: 'Machines', exact: true })).toBeFocused();
    await expect(page.getByRole('status').filter({ hasText: 'Unpaired' })).toHaveText(
      'Unpaired Leaver.'
    );
    const remove = api.calls.filter((call) => call.path === at(MachinePath, 'm-drop'));
    expect(remove).toHaveLength(1);
    expect(remove[0]).toMatchObject({ method: 'DELETE', csrf: '1' });
  });

  test('unpairing a machine that is already gone still removes it', async ({ page }) => {
    const api = await mockApi(page);
    api.state.machines.machines = [machine({ id: 'm-drop', name: 'Leaver' })];

    await page.goto('/machines');
    await expect(page.locator('[data-machine-id="m-drop"]')).toBeVisible();
    // Unpaired in another tab meanwhile: the server answers 404.
    api.state.machines.machines = [];
    await page.getByRole('button', { name: 'Unpair Leaver' }).click();
    await page.getByRole('button', { name: 'Confirm unpair Leaver' }).click();

    await expect(page.locator('[data-machine-id="m-drop"]')).toHaveCount(0);
    await expect(page.getByRole('alert')).toHaveCount(0);
  });

  test('a refresh already in flight does not bring back an unpaired machine', async ({ page }) => {
    const api = await mockApi(page);
    api.state.machines.machines = [
      machine({ id: 'm-keep', name: 'Keeper' }),
      machine({ id: 'm-drop', name: 'Leaver' })
    ];
    // Lists answer with the machines as they were when the request arrived,
    // but while `held` is set, only once the spec opens it.
    let held: ReturnType<typeof gate> | undefined;
    let listed = 0;
    await page.route(
      (url) => url.pathname === MachinesPath,
      async (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        listed += 1;
        const body = JSON.stringify(api.state.machines);
        await held?.wait;
        await route.fulfill({ status: 200, contentType: 'application/json', body });
      }
    );

    await page.clock.install({ time: now * 1000 });
    await page.goto('/machines');
    await expect(page.locator('[data-machine-id="m-drop"]')).toBeVisible();
    await page.getByRole('button', { name: 'Unpair Leaver' }).click();

    // The background refresh asks while Leaver is still paired...
    held = gate();
    const before = listed;
    await page.clock.runFor(10_000);
    await expect.poll(() => listed).toBe(before + 1);
    const stale = page.waitForResponse((response) => response.url().endsWith(MachinesPath));

    // ...and answers only after the unpair is done.
    await page.getByRole('button', { name: 'Confirm unpair Leaver' }).click();
    await expect(page.locator('[data-machine-id="m-drop"]')).toHaveCount(0);
    held.open();
    await stale;
    await page.waitForTimeout(300);
    expect(await page.locator('[data-machine-id="m-drop"]').count()).toBe(0);
    await expect(page.locator('[data-machine-id="m-keep"]')).toBeVisible();
  });

  test('the paired banner follows the machine', async ({ page }) => {
    const api = await mockApi(page);
    api.state.pairings[code] = pairingInfo;

    await page.clock.install({ time: now * 1000 });
    await page.goto(`/machines?code=${code}`);
    await page.getByRole('button', { name: 'Pair', exact: true }).click();
    const banner = page.getByRole('status').filter({ hasText: 'Paired' });
    await expect(banner).toContainText('review and approve the transcribe bundle');

    // The owner approves the bundle in the moil app; the list refreshes.
    api.state.machines.machines = api.state.machines.machines.map((item) => ({
      ...item,
      state: MachineIdle,
      last_seen_at: now,
      approved: true
    }));
    await page.clock.runFor(10_500);
    await expect(banner).toHaveText(
      'Paired Ada’s MacBook Pro. It will transcribe every new recording of rooms you own.'
    );
    await expect(page.getByTestId('approve-hint')).toHaveCount(0);

    await page.getByRole('button', { name: 'Unpair Ada’s MacBook Pro' }).click();
    await page.getByRole('button', { name: 'Confirm unpair Ada’s MacBook Pro' }).click();
    await expect(page.locator('#paired-banner')).toHaveCount(0);
  });

  test('a failed list says what went wrong and loads again', async ({ page }) => {
    await mockApi(page);
    await failNext(page, MachinesPath, { error: 'Could not load your machines. Try again.' });

    await page.goto('/machines');
    await expect(page.getByRole('alert')).toHaveText('Could not load your machines. Try again.');
    await page.getByRole('button', { name: 'Try again' }).click();
    await expect(page.getByRole('heading', { name: 'Machines', exact: true })).toBeFocused();
    await expect(page.getByTestId('onboarding')).toBeVisible();
  });

  test('an expired session on Unpair asks to sign in again', async ({ page }) => {
    const api = await mockApi(page);
    api.state.machines.machines = [machine({ id: 'm-drop', name: 'Leaver' })];

    await page.goto('/machines');
    await page.getByRole('button', { name: 'Unpair Leaver' }).click();
    api.state.signedIn = false;
    await page.getByRole('button', { name: 'Confirm unpair Leaver' }).click();
    await expect(page.getByRole('heading', { name: 'Sign in to see your machines' })).toBeFocused();
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

  test('fits a phone', async ({ page }) => {
    const api = await mockApi(page);
    api.state.machines.machines = [
      machine({ id: 'm-a', name: 'A machine with a long name that has to wrap somewhere' }),
      machine({ id: 'm-b', name: 'Old iMac', state: MachineOffline, approved: false })
    ];
    api.state.pairings[code] = pairingInfo;

    await page.setViewportSize({ width: 375, height: 800 });
    await page.goto('/machines');
    await expect(page.locator('[data-machine-id="m-b"]')).toBeVisible();
    expect(await clipped(page.locator('[data-machine-id]'))).toEqual([]);
    expect(await clipped(page.locator('main'))).toEqual([]);
    expect(await pageOverflows(page)).toBe(false);

    await page.goto(`/machines?code=${code}`);
    await expect(page.getByTestId('pairing-code')).toBeVisible();
    expect(await clipped(page.locator('main section'))).toEqual([]);
    expect(await pageOverflows(page)).toBe(false);
  });
});

test.describe('transcripts in the recordings list', () => {
  const recordingsPath = at(RoomRecordingsPath, 'standup');
  const row = (page: Page, id: string) => page.locator(`[data-recording-id="${id}"]`);
  const cell = (page: Page, id: string) => row(page, id).getByTestId('transcript');
  const note = (page: Page, id: string) => row(page, id).getByTestId('transcript-note');
  const live = (page: Page, id: string) => row(page, id).getByTestId('transcript-live');

  /** Every status, with the server's longest messages. */
  function everyStatus() {
    return [
      recording('none', wireNull),
      recording('available', { status: 'available' }),
      recording('waiting', {
        status: 'waiting',
        message: 'No paired machine has approved the transcriber yet. Approve it in the moil app.'
      }),
      recording('running', { status: 'running', progress: 0.42, message: 'Finding speakers' }),
      recording('completed', { status: 'completed', speakers: 12 }),
      recording('failed', { status: 'failed', error: setupError }),
      recording('video', wireNull, { audio_only: false })
    ];
  }

  test('renders every transcript status, reasons in full', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = everyStatus();

    await page.goto('/rooms/standup');
    await expect(row(page, 'none')).toBeVisible();

    await expect(cell(page, 'none')).toBeEmpty();
    await expect(note(page, 'none')).toHaveCount(0);
    // A completed recording is the usual case: it shows no status of its own.
    await expect(row(page, 'none')).not.toContainText(/completed/i);
    await expect(
      cell(page, 'available').getByRole('button', { name: /^Transcribe the recording from / })
    ).toBeVisible();

    // Each status names the transcript, so it can't be read as the recording's.
    await expect(cell(page, 'waiting')).toContainText('Transcript queued');
    await expect(note(page, 'waiting')).toHaveText(
      'No paired machine has approved the transcriber yet. Approve it in the moil app.'
    );

    await expect(cell(page, 'running')).toContainText('Transcribing 42%');
    await expect(note(page, 'running')).toHaveText('Finding speakers');

    await expect(cell(page, 'completed')).toContainText('12 speakers');
    await expect(
      cell(page, 'completed').getByRole('link', { name: /^Download transcript of the recording/ })
    ).toHaveAttribute('href', `${at(RecordingTranscriptPath, 'completed')}/download?format=txt`);
    await expect(
      cell(page, 'completed').getByRole('link', { name: /^Download captions of the recording/ })
    ).toHaveAttribute('href', `${at(RecordingTranscriptPath, 'completed')}/download?format=vtt`);

    await expect(cell(page, 'failed')).toContainText('Transcript failed');
    await expect(note(page, 'failed')).toHaveText(setupError);
    await expect(cell(page, 'failed').getByRole('button', { name: /^Retry/ })).toBeVisible();

    // Reasons are read in the row, not hidden in tooltips.
    for (const id of ['waiting', 'failed']) {
      await expect(note(page, id)).toBeVisible();
      const shown = await note(page, id).evaluate((element) => ({
        clipped: element.scrollWidth > element.clientWidth,
        titles: element.closest('[data-recording-id]')?.querySelectorAll('[title]').length
      }));
      expect(shown).toEqual({ clipped: false, titles: 0 });
    }
    expect(api.calls.filter((call) => call.method !== 'GET')).toHaveLength(0);
  });

  test('the transcript column appears only when a recording has a transcript', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('a', wireNull), recording('b', wireNull)];
    await page.setViewportSize({ width: 1280, height: 720 });

    const columns = () =>
      row(page, 'a').evaluate((element) =>
        getComputedStyle(element).gridTemplateColumns.split(' ')
      );
    await page.goto('/rooms/standup');
    await expect(row(page, 'a')).toBeVisible();
    expect(await columns()).toHaveLength(5);

    api.state.recordings[1].transcript = { status: 'completed', speakers: 2 };
    await page.reload();
    await expect(cell(page, 'b')).toContainText('2 speakers');
    const tracks = await columns();
    expect(tracks).toHaveLength(6);
    expect(tracks[4]).toBe('248px');
  });

  test('Transcribe requests a transcript and the row follows it', async ({ page }) => {
    const api = await mockApi(page);
    api.state.machines.machines = [machine({ state: MachineOffline })];
    api.state.recordings = [recording('rec-1', { status: 'available' })];

    await page.goto('/rooms/standup');
    await expect(live(page, 'rec-1')).toHaveText('');
    await cell(page, 'rec-1')
      .getByRole('button', { name: /^Transcribe/ })
      .click();

    await expect(cell(page, 'rec-1')).toHaveAttribute('data-status', 'waiting');
    await expect(note(page, 'rec-1')).toHaveText(offlineMessage);
    // The button is gone; focus stays on its cell, and the change is read out.
    await expect(cell(page, 'rec-1')).toBeFocused();
    await expect(live(page, 'rec-1')).toHaveText(/^Transcript queued: recording from /);
    const posts = api.calls.filter((call) => call.path === at(RecordingTranscriptPath, 'rec-1'));
    expect(posts).toHaveLength(1);
    expect(posts[0]).toMatchObject({ method: 'POST', csrf: '1' });
  });

  test('Retry requests a failed transcript again', async ({ page }) => {
    const api = await mockApi(page);
    api.state.machines.machines = [machine({ state: MachineIdle })];
    api.state.recordings = [recording('rec-2', { status: 'failed', error: setupError })];

    await page.clock.install({ time: now * 1000 });
    await page.goto('/rooms/standup');
    await cell(page, 'rec-2')
      .getByRole('button', { name: /^Retry/ })
      .click();

    await expect(cell(page, 'rec-2')).toHaveAttribute('data-status', 'waiting');
    await expect(note(page, 'rec-2')).toHaveText('Waiting for a machine to start it.');
    await expect(cell(page, 'rec-2')).toBeFocused();
    const posts = api.calls.filter((call) => call.path === at(RecordingTranscriptPath, 'rec-2'));
    expect(posts).toHaveLength(1);
    expect(posts[0]).toMatchObject({ method: 'POST', csrf: '1' });
  });

  test('asking for a transcript already under way shows where it is', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('rec-4', { status: 'available' })];

    await page.clock.install({ time: now * 1000 });
    await page.goto('/rooms/standup');
    await expect(cell(page, 'rec-4')).toHaveAttribute('data-status', 'available');
    // Meanwhile a machine took it; the request answers with its status.
    api.state.recordings[0].transcript = {
      status: 'running',
      progress: 0.3,
      message: 'Transcribing'
    };
    await cell(page, 'rec-4')
      .getByRole('button', { name: /^Transcribe/ })
      .click();

    await expect(cell(page, 'rec-4')).toHaveAttribute('data-status', 'running');
    await expect(cell(page, 'rec-4')).toContainText('30%');
    await expect(page.getByRole('alert')).toHaveCount(0);
  });

  test('a conflict loads the list again', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('rec-5', { status: 'available' })];

    await page.goto('/rooms/standup');
    await expect(cell(page, 'rec-5')).toHaveAttribute('data-status', 'available');
    const loads = api.count('GET', recordingsPath);
    // Transcribed meanwhile, from another tab: the server answers 409.
    api.state.recordings[0].transcript = { status: 'completed', speakers: 2 };
    await cell(page, 'rec-5')
      .getByRole('button', { name: /^Transcribe/ })
      .click();

    await expect(page.getByRole('alert')).toHaveText('This recording already has a transcript.');
    await expect(cell(page, 'rec-5')).toHaveAttribute('data-status', 'completed');
    await expect(cell(page, 'rec-5')).toContainText('2 speakers');
    expect(api.count('GET', recordingsPath)).toBe(loads + 1);
  });

  test('an expired session on Transcribe asks to sign in again', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('rec-6', { status: 'available' })];

    await page.goto('/rooms/standup');
    await expect(cell(page, 'rec-6')).toHaveAttribute('data-status', 'available');
    api.state.signedIn = false;
    await cell(page, 'rec-6')
      .getByRole('button', { name: /^Transcribe/ })
      .click();
    await expect(page.getByText('Sign in from the dashboard to manage this room.')).toBeVisible();
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
    api.state.recordings[0].transcript = {
      status: 'waiting',
      message: 'Waiting for a paired machine to finish its current job.'
    };
    expect(await after(3_100)).toBe(first + 2);
    await expect(note(page, 'rec-3')).toHaveText(
      'Waiting for a paired machine to finish its current job.'
    );
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

  test('a failed poll keeps the list and tries again more slowly', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('rec-7', { status: 'running', progress: 0.1 })];
    const loads = () => api.count('GET', recordingsPath);

    await page.clock.install({ time: now * 1000 });
    await page.goto('/rooms/standup');
    await expect(cell(page, 'rec-7')).toContainText('10%');
    await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 100);
    async function after(ms: number): Promise<number> {
      await page.clock.runFor(ms);
      await page.waitForTimeout(250);
      return loads();
    }

    // The next poll fails (klisi restarting, say); the mock never sees it.
    let failed = 0;
    await page.route(
      (url) => url.pathname === recordingsPath,
      async (route) => {
        if (failed > 0) return route.fallback();
        failed += 1;
        await route.fulfill({
          status: 502,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Bad gateway.' })
        });
      }
    );
    const first = loads();
    await page.clock.runFor(3_100);
    await expect.poll(() => failed).toBe(1);
    await page.waitForTimeout(250);
    await expect(page.getByRole('alert')).toHaveCount(0);
    await expect(cell(page, 'rec-7')).toContainText('10%');

    // It backs off to the slow pace, then picks up where it left off.
    api.state.recordings[0].transcript = { status: 'running', progress: 0.6 };
    expect(await after(3_100)).toBe(first);
    expect(await after(12_000)).toBe(first + 1);
    await expect(cell(page, 'rec-7')).toContainText('60%');
    expect(await after(3_100)).toBe(first + 2);
    await expect(page.getByRole('alert')).toHaveCount(0);
  });

  test('a failed first load says so and loads again', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('rec-8', { status: 'available' })];
    await failNext(page, recordingsPath, { status: 0 });

    await page.goto('/rooms/standup');
    await expect(page.getByRole('alert')).toHaveText(
      'Could not load the recordings. Check your connection, then try again.'
    );
    await expect(page.getByText('No recordings yet.')).toHaveCount(0);
    await page.getByRole('button', { name: 'Try again' }).click();
    await expect(cell(page, 'rec-8')).toHaveAttribute('data-status', 'available');
    await expect(page.getByRole('alert')).toHaveCount(0);
  });

  test('a hidden tab stops polling and catches up when shown', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('rec-9', { status: 'running', progress: 0.1 })];
    const loads = () => api.count('GET', recordingsPath);

    await page.clock.install({ time: now * 1000 });
    await page.goto('/rooms/standup');
    await expect(cell(page, 'rec-9')).toContainText('10%');
    await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 100);

    await setHidden(page, true);
    const first = loads();
    await page.clock.runFor(60_000);
    await page.waitForTimeout(250);
    expect(loads()).toBe(first);

    api.state.recordings[0].transcript = { status: 'completed', speakers: 3 };
    await setHidden(page, false);
    await expect(cell(page, 'rec-9')).toContainText('3 speakers');
    expect(loads()).toBe(first + 1);
  });

  test('the live region reads status words, not progress', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = [recording('rec-10', { status: 'running', progress: 0.1 })];

    await page.clock.install({ time: now * 1000 });
    await page.goto('/rooms/standup');
    await expect(live(page, 'rec-10')).toHaveAttribute('aria-live', 'polite');
    await expect(live(page, 'rec-10')).toHaveText(/^Transcribing: recording from /);

    api.state.recordings[0].transcript = { status: 'running', progress: 0.7 };
    await page.clock.runFor(3_100);
    await expect(cell(page, 'rec-10')).toContainText('70%');
    await expect(live(page, 'rec-10')).not.toContainText('%');

    api.state.recordings[0].transcript = { status: 'completed', speakers: 1 };
    await page.clock.runFor(3_100);
    await expect(live(page, 'rec-10')).toHaveText(/^Transcript ready: recording from /);
  });

  test('fits a phone, and Tab follows the lines', async ({ page }) => {
    const api = await mockApi(page);
    api.state.recordings = everyStatus();

    await page.setViewportSize({ width: 375, height: 900 });
    await page.goto('/rooms/standup');
    await expect(row(page, 'failed')).toBeVisible();

    expect(await clipped(page.locator('[data-recording-id]'))).toEqual([]);
    expect(await pageOverflows(page)).toBe(false);
    // The date keeps its words, and Download keeps a name without its label.
    for (const time of await page.locator('[data-recording-id] time').all()) {
      expect(await time.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(
        true
      );
    }
    const download = row(page, 'available').getByRole('link', {
      name: /^Download the recording from /
    });
    await expect(download).toBeVisible();
    await expect(download.getByText('Download')).toBeHidden();

    // The row's actions end its first line, the transcript has the next.
    const transcribe = row(page, 'available').getByRole('button', {
      name: /^Transcribe the recording from /
    });
    const remove = row(page, 'available').getByRole('button', {
      name: /^Delete the recording from /
    });
    await download.focus();
    await page.keyboard.press('Tab');
    await expect(remove).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(transcribe).toBeFocused();

    // Wide, the transcript has a column before the actions, and Tab follows
    // it there too (CSS reading-flow).
    await page.setViewportSize({ width: 1280, height: 900 });
    await transcribe.focus();
    await page.keyboard.press('Tab');
    await expect(download).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(remove).toBeFocused();
  });
});
