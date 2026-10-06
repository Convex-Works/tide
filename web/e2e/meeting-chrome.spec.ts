import { expect, test, type Locator, type Page } from '@playwright/test';
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

type Box = { x: number; y: number; width: number; height: number };

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
  // It gives the link, whole: not the name again, and not cut off.
  await expect(details).not.toContainText(roomName);
  const link = details.getByText(url, { exact: true });
  await expect(link).toBeVisible();
  expect(await link.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  const box = await details.boundingBox();
  const linkBox = await link.boundingBox();
  expect(linkBox!.x + linkBox!.width).toBeLessThanOrEqual(box!.x + box!.width);
  // Focus moves into the popover, onto its one control.
  const copy = details.getByRole('button', { name: 'Copy link' });
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
  await expect(details.getByRole('button', { name: 'Copied' })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(url);

  // Escape closes it and hands focus back to (i).
  await page.keyboard.press('Escape');
  await expect(details).toBeHidden();
  await expect(trigger).toBeFocused();

  // The REC chip joins the cluster.
  sfu.setRoomMetadata('{"recording":true}');
  await expect(cluster.getByTestId('recording-chip')).toBeVisible();
});

const phone = { width: 320, height: 640 };

/** Fails unless the element is drawn wholly inside the viewport, `slack` px in from the sides. */
async function expectInside(locator: Locator, slack = 0): Promise<void> {
  await expect(locator).toBeVisible();
  const box = await locator.boundingBox();
  expect(box, `${locator} has a box`).not.toBeNull();
  const { x, y, width, height } = box!;
  const where = `${locator} at x ${x}..${x + width}, y ${y}..${y + height}`;
  expect(x >= slack && x + width <= phone.width - slack, where).toBe(true);
  expect(y >= 0 && y + height <= phone.height, where).toBe(true);
}

function overlaps(a: Box, b: Box): boolean {
  return a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
}

/**
 * Puts the stage in the states that crowd its header: the browser refusing
 * audio until a gesture, and the connection dropping.
 */
async function blockPlaybackAndReconnect(page: Page): Promise<void> {
  await page.evaluate(() => {
    type TestRoom = {
      canPlaybackAudio: boolean;
      startAudio: () => Promise<void>;
      emit: (event: string, ...args: unknown[]) => boolean;
    };
    const room = (window as Window & { __tideRoom?: TestRoom }).__tideRoom!;
    Object.defineProperty(room, 'canPlaybackAudio', { configurable: true, get: () => false });
    room.startAudio = () => Promise.reject(new Error('Playback needs a gesture.'));
    room.emit('audioPlaybackChanged', false);
    room.emit('connectionStateChanged', 'reconnecting');
  });
}

test('on a phone the header keeps every chip on screen and clears the panels', async ({ page }) => {
  await page.setViewportSize(phone);
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
  // The whole host bar fits, with room to spare.
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });
  await expectInside(bar, 8);

  // All at once: REC, the autoplay unlock and the connection state.
  await blockPlaybackAndReconnect(page);
  const unlock = page.getByTestId('playback-blocked');
  const connection = page.locator('.stage-header').getByRole('status');
  await expect(unlock).toHaveText('Tap to hear audio');
  await expect(connection).toHaveText('Reconnecting…');
  const parts = [
    page.getByTestId('stage-clock'),
    page.getByRole('button', { name: 'Meeting details' }),
    page.getByTestId('recording-chip'),
    unlock,
    connection
  ];
  for (const part of parts) await expectInside(part);
  // The unlock is an instruction; it takes a second row rather than an ellipsis.
  expect(await unlock.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  const boxes = await Promise.all(parts.map(async (part) => (await part.boundingBox())!));
  for (const [i, a] of boxes.entries()) {
    for (const b of boxes.slice(i + 1)) expect(overlaps(a, b)).toBe(false);
  }
  // The header grew rather than spilling, and nothing in it reaches the media.
  const header = (await page.locator('.stage-header').boundingBox())!;
  const media = (await page.getByRole('region', { name: 'Meeting participants' }).boundingBox())!;
  expect(header.y + header.height).toBeLessThanOrEqual(media.y);

  // An open panel starts below the whole header, second row included.
  await page.getByRole('button', { name: 'Open people' }).click();
  const panel = (await page.getByRole('complementary', { name: 'People' }).boundingBox())!;
  for (const box of boxes) expect(panel.y).toBeGreaterThanOrEqual(box.y + box.height);
});

test('on a phone the confirm steps and device menus open on screen', async ({ page }) => {
  await page.setViewportSize(phone);
  await mockApi(page, state());
  await fakeSfu(page);
  await enter(page);
  const bar = page.getByRole('navigation', { name: 'Meeting controls' });

  // Record? opens its choice above the bar, with a confirm of its own.
  await page.getByRole('button', { name: 'Start recording' }).click();
  await expectInside(page.getByRole('button', { name: 'Record?' }), 8);
  const recordNow = page.getByRole('button', { name: 'Record', exact: true });
  const withVideo = page.getByLabel('Also record video');
  await expectInside(recordNow);
  await expectInside(withVideo);
  await expectInside(bar, 8);
  await page.keyboard.press('Escape');
  await expect(withVideo).toBeHidden();

  // They all open in the same place, so opening one closes the others.
  await page.getByRole('button', { name: 'Start recording' }).click();
  await expect(withVideo).toBeVisible();
  await page.getByRole('button', { name: 'Choose microphone' }).click();
  await expect(page.getByRole('menu', { name: 'Choose microphone' })).toBeVisible();
  await expect(withVideo).toBeHidden();
  await page.getByRole('button', { name: 'Leave or end meeting' }).click();
  await expect(page.getByRole('button', { name: 'End meeting for all' })).toBeVisible();
  await expect(page.getByRole('menu', { name: 'Choose microphone' })).toBeHidden();
  await page.getByRole('button', { name: 'Start recording' }).click();
  await expect(withVideo).toBeVisible();
  await expect(page.getByRole('button', { name: 'End meeting for all' })).toBeHidden();
  await page.keyboard.press('Escape');

  await page.getByRole('button', { name: 'Start recording' }).click();
  await withVideo.check();
  const [start] = await Promise.all([
    page.waitForRequest((request) => request.url().endsWith('/api/rooms/standup/recording/start')),
    recordNow.click()
  ]);
  expect(start.postDataJSON()).toEqual({ video: true });
  await expect(withVideo).toBeHidden();

  await page.getByRole('button', { name: 'Leave or end meeting' }).click();
  await expectInside(page.getByRole('button', { name: 'Leave room' }));
  await expectInside(page.getByRole('button', { name: 'End meeting for all' }));
  await expectInside(bar, 8);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('button', { name: 'End meeting for all' })).toBeHidden();

  for (const kind of ['microphone', 'camera']) {
    await page.getByRole('button', { name: `Choose ${kind}` }).click();
    await expectInside(page.getByRole('menu', { name: `Choose ${kind}` }));
    await page.keyboard.press('Escape');
  }
});

test('names and the clock are in Inter, and roles are words, not badges', async ({ page }) => {
  await mockApi(page, state());
  await fakeSfu(page, { metadata: '{"role":"host"}' });
  await enter(page);

  const inter = (locator: Locator) =>
    locator.evaluate((element) => getComputedStyle(element).fontFamily.split(',')[0].trim());
  expect(await inter(page.getByTestId('stage-clock'))).toMatch(/Inter/);

  const tile = page.locator('[data-testid="participant-tile"]').first();
  await expect(tile).toContainText('Ada Host (You)');
  expect(await inter(tile.getByText('(You)'))).toMatch(/Inter/);
  await expect(tile).not.toContainText('YOU');

  await page.getByRole('button', { name: /people/i }).click();
  const row = page.locator('.participant-row').first();
  await expect(row).toContainText('Ada Host (You)');
  const role = row.getByText('Meeting host', { exact: true });
  await expect(role).toBeVisible();
  // Secondary text, with no pill around it.
  expect(
    await role.evaluate((element) => {
      const style = getComputedStyle(element);
      return style.borderStyle === 'none' && style.borderRadius === '0px';
    })
  ).toBe(true);
});
