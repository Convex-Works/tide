import { expect, test } from '@playwright/test';
import {
  MachinesPath,
  PairingPath,
  RoomRecordingsPath,
  TranscriptAvailable,
  TranscriptCompleted
} from '../src/lib/api/types.gen';
import { defaultState, machine, mockApi, recording } from './mock-api';

// A server with TIDE_TRANSCRIPTS off (ARCHITECTURE.md §8.1): /api/me says
// so, the machine, pairing and transcript routes 404, and the SPA shows no
// way to them. Against the Vite dev server with the API mocked in the browser.

function transcriptsOff() {
  const state = defaultState();
  state.me.transcripts = false;
  // What the server kept from when transcripts were on: still there, never sent.
  state.machines.machines = [machine()];
  state.recordings = [
    recording('done', { status: TranscriptCompleted, speakers: 3 }),
    recording('old', { status: TranscriptAvailable })
  ];
  return state;
}

test('the dashboard has no Machines link', async ({ page }) => {
  await mockApi(page, transcriptsOff());
  await page.goto('/');
  await expect(page.getByTestId('room-card')).toBeVisible();
  await expect(page.getByText('Ada Host')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Machines' })).toHaveCount(0);
});

test('/machines says transcripts are not enabled, and asks for no machines', async ({ page }) => {
  const api = await mockApi(page, transcriptsOff());
  await page.goto('/machines');
  await expect(
    page.getByRole('heading', { name: "Transcripts aren't enabled on this server" })
  ).toBeVisible();
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Add a machine' })).toHaveCount(0);
  expect(api.count('GET', MachinesPath)).toBe(0);
});

test('a pairing link shows the same, and looks up no code', async ({ page }) => {
  const api = await mockApi(page, transcriptsOff());
  await page.goto('/machines?code=WDJB-MJHT');
  await expect(
    page.getByRole('heading', { name: "Transcripts aren't enabled on this server" })
  ).toBeVisible();
  await expect(page.getByRole('button', { name: 'Pair' })).toHaveCount(0);
  expect(api.calls.filter((call) => call.path.startsWith(PairingPath.split('{')[0]))).toEqual([]);
});

test('a room lists its recordings without transcript controls', async ({ page }) => {
  const api = await mockApi(page, transcriptsOff());
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto('/rooms/standup');

  const done = page.locator('[data-recording-id="done"]');
  await expect(done).toBeVisible();
  await expect(page.locator('[data-recording-id="old"]')).toBeVisible();
  expect(api.count('GET', RoomRecordingsPath.replace('{slug}', 'standup'))).toBeGreaterThan(0);
  // No transcript column, cell, download or request.
  const tracks = await done.evaluate((element) =>
    getComputedStyle(element).gridTemplateColumns.split(' ')
  );
  expect(tracks).toHaveLength(5);
  await expect(page.getByTestId('transcript').filter({ visible: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /Transcribe/ })).toHaveCount(0);
  await expect(page.getByRole('link', { name: /transcript/i })).toHaveCount(0);
  await expect(page.getByText(/speakers?/)).toHaveCount(0);
});

test('the same server with transcripts on shows them all', async ({ page }) => {
  const state = transcriptsOff();
  state.me.transcripts = true;
  await mockApi(page, state);
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Machines' })).toBeVisible();
  await page.goto('/rooms/standup');
  await expect(page.locator('[data-recording-id="done"]').getByTestId('transcript')).toContainText(
    '3 speakers'
  );
  await expect(page.getByRole('button', { name: /Transcribe/ })).toBeVisible();
});
