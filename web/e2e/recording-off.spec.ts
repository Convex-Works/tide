import { expect, test, type Page } from '@playwright/test';
import { RoomRecordingsPath } from '../src/lib/api/types.gen';
import { fakeSfu, fakeSfuLaunchArgs } from './fake-sfu';
import { defaultState, mockApi, recording } from './mock-api';

// A signed-in server without object storage (ARCHITECTURE.md §8): /api/me
// says `recording: false`, the recording routes 404, and the SPA offers no
// way to them. Against the Vite dev server with the API mocked in the browser.

test.use({ launchOptions: { args: fakeSfuLaunchArgs } });

function recordingOff() {
  const state = defaultState();
  state.me.recording = false;
  state.me.transcripts = false;
  // What the server kept from when recording was on: still there, never sent.
  state.recordings = [recording('kept', undefined)];
  return state;
}

function controlLabels(page: Page): Promise<string[]> {
  return page
    .getByRole('navigation', { name: 'Meeting controls' })
    .evaluate((bar) =>
      [...bar.querySelectorAll('button')].map((button) => button.getAttribute('aria-label') ?? '')
    );
}

test('a host in the meeting has no record control', async ({ page }) => {
  await mockApi(page, recordingOff());
  await fakeSfu(page, { metadata: '{"role":"host"}' });
  await page.goto('/m/standup');
  for (const control of ['camera', 'microphone']) {
    const toggle = page.locator(`button[aria-pressed][aria-label*="${control}" i]`).first();
    if ((await toggle.getAttribute('aria-pressed')) === 'true') await toggle.click();
  }
  await page.getByRole('button', { name: 'Join meeting' }).click();
  await expect(page.getByRole('button', { name: 'Share screen' })).toBeVisible({ timeout: 20_000 });

  // Still the host: they can end the meeting, but not record it.
  expect(await controlLabels(page)).toEqual([
    'Unmute microphone',
    'Choose microphone',
    'Turn camera on',
    'Choose camera',
    'Share screen',
    'Speaker view',
    'Open people',
    'Open chat',
    'Leave or end meeting'
  ]);
});

test('a room has no recordings section and asks for none', async ({ page }) => {
  const api = await mockApi(page, recordingOff());
  await page.goto('/rooms/standup');

  await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Recordings' })).toHaveCount(0);
  await expect(page.locator('[data-recording-id]')).toHaveCount(0);
  await expect(page.getByText('Removes the room and its link.', { exact: false })).toBeVisible();
  await expect(page.getByText(/recording/i)).toHaveCount(0);
  expect(api.count('GET', RoomRecordingsPath.replace('{slug}', 'standup'))).toBe(0);
});

test('the same room with recording on lists its recordings', async ({ page }) => {
  const state = recordingOff();
  state.me.recording = true;
  const api = await mockApi(page, state);
  await page.goto('/rooms/standup');

  await expect(page.getByRole('heading', { name: 'Recordings' })).toBeVisible();
  await expect(page.locator('[data-recording-id="kept"]')).toBeVisible();
  await expect(page.getByText('and every recording', { exact: false })).toBeVisible();
  expect(api.count('GET', RoomRecordingsPath.replace('{slug}', 'standup'))).toBeGreaterThan(0);
});
