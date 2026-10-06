import { expect, test, type Page } from '@playwright/test';
import { MePath, RoomJoinPath, RoomsPath, type JoinRequest } from '../src/lib/api/types.gen';
import { fakeSfu, fakeSfuLaunchArgs } from './fake-sfu';
import { defaultState, mockApi, type ApiState } from './mock-api';

// A server without sign-in (ARCHITECTURE.md §4.1): /api/me issues a browser
// with no session an anonymous one instead of answering 401, the creator of a
// room owns it, and there is no account, sign-in or sign-out anywhere.
// Against the Vite dev server with the API mocked in the browser.

test.use({ launchOptions: { args: fakeSfuLaunchArgs } });

/** A browser that has never been here, on a server without sign-in. */
function anonymous(): ApiState {
  const state = defaultState();
  state.signedIn = false;
  state.me = {
    sub: 'anon:abcdefghijklmnopqrstuvwxyz',
    email: '',
    name: '',
    transcripts: false,
    recording: false,
    anonymous: true
  };
  return state;
}

/** Pre-join with capture off (nothing is published to the fake SFU). */
async function captureOff(page: Page): Promise<void> {
  for (const control of ['camera', 'microphone']) {
    const toggle = page.locator(`button[aria-pressed][aria-label*="${control}" i]`).first();
    await expect(toggle).toBeVisible();
    if ((await toggle.getAttribute('aria-pressed')) === 'true') await toggle.click();
    await expect(toggle).toHaveAttribute('aria-pressed', 'false');
  }
}

test('a new browser gets the dashboard, with no account and no sign-in', async ({ page }) => {
  const state = anonymous();
  state.rooms = [];
  await mockApi(page, state);
  await page.goto('/');

  await expect(page.getByRole('heading', { name: 'Rooms' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'New', exact: true })).toBeVisible();
  await expect(page.getByText('Create your first room')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Sign out' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Continue with SSO' })).toHaveCount(0);
  await expect(page.getByText(/sign (in|out)|SSO/i)).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Machines' })).toHaveCount(0);
});

test('the creator names themselves and joins their room as its host', async ({ page }) => {
  const state = anonymous();
  state.rooms = [];
  const api = await mockApi(page, state);
  await fakeSfu(page, { metadata: '{"role":"host"}' });
  await page.goto('/');

  await page.getByRole('button', { name: 'New', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'New room' });
  await dialog.getByRole('textbox', { name: 'Link' }).fill('quick-sync');
  await dialog.getByRole('textbox', { name: 'Name' }).fill('Quick sync');
  await dialog.getByRole('button', { name: 'Create & join' }).click();
  await page.waitForURL('**/m/quick-sync');
  expect(api.count('POST', RoomsPath)).toBe(1);

  // No account to show: the name is theirs to type, as a guest's is, and
  // the only account word is the role they join with.
  const name = page.locator('input[name="name"]');
  await expect(name).toHaveValue('');
  await expect(page.getByText('Host', { exact: true })).toBeVisible();
  await expect(page.getByText(/sign (in|out)/i)).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Sign out' })).toHaveCount(0);

  await captureOff(page);
  await page.getByRole('button', { name: 'Join meeting' }).click();
  // A required name keeps them on pre-join until they type one.
  await expect(page.getByRole('button', { name: 'Share screen' })).toHaveCount(0);
  expect(api.count('POST', RoomJoinPath.replace('{slug}', 'quick-sync'))).toBe(0);

  const joins: JoinRequest[] = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && request.url().endsWith('/api/rooms/quick-sync/join')) {
      joins.push(request.postDataJSON() as JoinRequest);
    }
  });
  await name.fill('Ann');
  await page.getByRole('button', { name: 'Join meeting' }).click();
  await expect(page.getByRole('button', { name: 'Share screen' })).toBeVisible({ timeout: 20_000 });
  expect(joins).toEqual([{ name: 'Ann' }]);

  await expect(page.locator('[data-testid="participant-tile"]').first()).toContainText('Ann (You)');
  // The owner can end the meeting; there is no recording to start.
  await expect(page.getByRole('button', { name: 'Leave or end meeting' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Start recording' })).toHaveCount(0);
});

test('a guest sees no sign-in, and waits in the lobby', async ({ page }) => {
  // Someone else's room: this browser's new session owns nothing.
  const api = await mockApi(page, anonymous());
  await page.goto('/m/standup');

  await expect(page.locator('input[name="name"]')).toHaveValue('');
  expect(api.count('GET', MePath)).toBe(1);
  await expect(page.getByRole('link', { name: 'Sign in' })).toHaveCount(0);
  await expect(page.getByText('Host', { exact: true })).toHaveCount(0);

  await captureOff(page);
  await page.locator('input[name="name"]').fill('Grace Guest');
  await page.getByRole('button', { name: 'Join meeting' }).click();
  await expect(
    page.getByRole('heading', { name: 'Waiting for the host to let you in.' })
  ).toBeVisible();
});

/** Issuing a session counts against the login limit (§15); this one is refused. */
async function refuseSession(page: Page): Promise<void> {
  await page.route(
    (url) => url.pathname === MePath,
    (route) =>
      route.fulfill({
        status: 429,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Too many requests. Try again later.' })
      })
  );
}

test('a guest refused a session is still a guest, with no sign-in', async ({ page }) => {
  await mockApi(page, anonymous());
  await refuseSession(page);
  await page.goto('/m/standup');

  await expect(page.locator('input[name="name"]')).toHaveValue('');
  await expect(page.getByRole('button', { name: 'Join meeting' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Sign in' })).toHaveCount(0);
  await expect(page.getByText(/sign (in|out)/i)).toHaveCount(0);
});

test('a refused session says to try again in a minute, not to sign in', async ({ page }) => {
  await mockApi(page, anonymous());
  await refuseSession(page);
  await page.goto('/');

  await expect(page.getByRole('alert')).toHaveText(
    'Too many requests from this network. Try again in a minute.'
  );
  await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
  await expect(page.getByText(/sign (in|out)|SSO/i)).toHaveCount(0);
});
