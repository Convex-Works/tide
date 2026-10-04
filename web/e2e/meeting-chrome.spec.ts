import { expect, test, type Page } from '@playwright/test';
import { defaultState, mockApi, type ApiState } from './mock-api';
import { fakeSfu, fakeSfuLaunchArgs } from './fake-sfu';

// The meeting's own chrome (ARCHITECTURE.md §9 "Meeting chrome"): the control
// bar's order and the stage's top-left cluster. The API is mocked and the SFU
// is fake-sfu.ts, so this runs against the Vite dev server alone.

test.use({
  launchOptions: { args: fakeSfuLaunchArgs },
  locale: 'en-GB',
  timezoneId: 'UTC',
  permissions: ['clipboard-read', 'clipboard-write']
});

const roomName = 'Weekly product sync';

function state(overrides: { signedIn?: boolean; name?: string; lobby?: boolean } = {}): ApiState {
  const base = defaultState();
  base.signedIn = overrides.signedIn ?? true;
  base.rooms[0] = {
    ...base.rooms[0],
    name: overrides.name ?? roomName,
    lobby_enabled: overrides.lobby ?? true
  };
  return base;
}

/** Pre-join with capture off (nothing is published to the fake SFU), then join. */
async function enter(page: Page, guestName?: string): Promise<void> {
  await page.goto('/m/standup');
  if (guestName) await page.locator('input[name="name"]').fill(guestName);
  for (const control of ['camera', 'microphone']) {
    const toggle = page.locator(`button[aria-pressed][aria-label*="${control}" i]`).first();
    await expect(toggle).toBeVisible();
    if ((await toggle.getAttribute('aria-pressed')) === 'true') await toggle.click();
    await expect(toggle).toHaveAttribute('aria-pressed', 'false');
  }
  await page.getByRole('button', { name: 'Join meeting' }).click();
  await expect(page.getByRole('button', { name: 'Share screen' })).toBeVisible({ timeout: 20_000 });
}

function controlLabels(page: Page): Promise<string[]> {
  return page
    .getByRole('navigation', { name: 'Meeting controls' })
    .evaluate((bar) =>
      [...bar.querySelectorAll('button')].map((button) => button.getAttribute('aria-label') ?? '')
    );
}

test('a host has microphone and camera side by side, and record after screen share', async ({
  page
}) => {
  await mockApi(page, state());
  await fakeSfu(page);
  await enter(page);

  expect(await controlLabels(page)).toEqual([
    'Unmute microphone',
    'Choose microphone',
    'Turn camera on',
    'Choose camera',
    'Share screen',
    'Start recording',
    'Speaker view',
    'Open people',
    'Open chat',
    'Leave or end meeting'
  ]);

  // Record's confirm step still opens in its new place, after screen share.
  await page.getByRole('button', { name: 'Start recording' }).click();
  const labels = await controlLabels(page);
  expect(labels.indexOf('Record?')).toBe(labels.indexOf('Share screen') + 1);
  await expect(page.getByLabel('Also record video')).toBeVisible();
});

test('a guest has microphone and camera in the same place as a host, and no record', async ({
  page
}) => {
  await mockApi(page, state({ signedIn: false, lobby: false }));
  await fakeSfu(page);
  await enter(page, 'Grace Guest');

  expect(await controlLabels(page)).toEqual([
    'Unmute microphone',
    'Choose microphone',
    'Turn camera on',
    'Choose camera',
    'Share screen',
    'Speaker view',
    'Open people',
    'Open chat',
    'Leave room'
  ]);
});

test('the clock shows the time and turns over on the minute', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-10-04T23:58:00Z') });
  await mockApi(page, state());
  await fakeSfu(page);
  await enter(page);

  const clock = page.getByTestId('stage-clock');
  await expect(clock).toHaveText('23:58');

  await page.clock.pauseAt(new Date('2026-10-04T23:59:59Z'));
  await expect(clock).toHaveText('23:59');
  // Still the old minute a millisecond before the boundary, the new one on it.
  await page.clock.runFor(999);
  await expect(clock).toHaveText('23:59');
  await page.clock.runFor(1);
  await expect(clock).toHaveText('00:00');
  await expect(clock).toHaveAttribute('datetime', '00:00');
  await page.clock.runFor(60_000);
  await expect(clock).toHaveText('00:01');
});

test.describe('in a 12-hour locale', () => {
  test.use({ locale: 'en-US' });

  test('the clock follows the viewer’s locale', async ({ page }) => {
    await page.clock.install({ time: new Date('2026-10-04T23:58:00Z') });
    await mockApi(page, state());
    await fakeSfu(page);
    await enter(page);
    await expect(page.getByTestId('stage-clock')).toHaveText('11:58 PM');
  });
});

test('the stage names the room, and its details give the meeting link to copy', async ({
  page
}) => {
  await mockApi(page, state());
  const sfu = await fakeSfu(page);
  await enter(page);

  const cluster = page.getByTestId('stage-cluster');
  await expect(cluster.getByRole('heading', { name: roomName })).toBeVisible();
  // The slug is kept out of the meeting UI; only the details' URL carries it.
  await expect(cluster).not.toContainText('standup');

  const url = `${new URL(page.url()).origin}/m/standup`;
  const trigger = page.getByRole('button', { name: 'Meeting details' });
  await trigger.click();
  const details = page.getByRole('dialog', { name: 'Meeting details' });
  await expect(details).toBeVisible();
  await expect(details).toContainText(roomName);
  await expect(details).toContainText(url);
  // Focus moves into the popover, onto its one control.
  const copy = details.getByRole('button', { name: 'Copy meeting link' });
  await expect(copy).toBeFocused();

  // A click anywhere else closes it. bits-ui ignores outside clicks for a few
  // ms after opening, quicker than any person but not than Playwright, so the
  // click is repeated until the popover has settled.
  await expect(async () => {
    await page.mouse.click(1000, 400);
    await expect(details).toBeHidden({ timeout: 250 });
  }).toPass({ timeout: 5_000 });

  await trigger.click();
  await copy.click();
  await expect(details.getByRole('status')).toHaveText('Link copied');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(url);

  // Escape closes it and hands focus back to (i).
  await page.keyboard.press('Escape');
  await expect(details).toBeHidden();
  await expect(trigger).toBeFocused();

  // The REC chip joins the cluster.
  sfu.setRoomMetadata('{"recording":true}');
  await expect(cluster.getByTestId('recording-chip')).toBeVisible();
});

test('on a phone the cluster ellipsizes a long name and clears the panels', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 });
  await mockApi(
    page,
    state({ name: 'Quarterly planning for the platform team and everyone who joins late' })
  );
  const sfu = await fakeSfu(page);
  await enter(page);
  sfu.setRoomMetadata('{"recording":true}');
  await expect(page.getByTestId('recording-chip')).toBeVisible();

  const name = page.getByTestId('stage-cluster').getByRole('heading');
  expect(await name.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
  for (const part of [
    page.getByTestId('stage-clock'),
    page.getByRole('button', { name: 'Meeting details' }),
    page.getByTestId('recording-chip')
  ]) {
    const box = await part.boundingBox();
    expect(box && box.x >= 0 && box.x + box.width <= 320).toBe(true);
  }

  // The whole host bar fits, and an open panel starts below the cluster.
  const bar = await page.getByRole('navigation', { name: 'Meeting controls' }).boundingBox();
  expect(bar && bar.x >= 0 && bar.x + bar.width <= 320).toBe(true);
  await page.getByRole('button', { name: 'Open people' }).click();
  const panel = await page.getByRole('complementary', { name: 'People' }).boundingBox();
  const cluster = await page.getByTestId('stage-cluster').boundingBox();
  expect(panel && cluster && panel.y >= cluster.y + cluster.height).toBe(true);
});
