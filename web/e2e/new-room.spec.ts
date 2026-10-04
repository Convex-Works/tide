import { readFile } from 'node:fs/promises';
import { expect, test, type Download, type Page } from '@playwright/test';
import { RoomsPath, type CreateRoomRequest } from '../src/lib/api/types.gen';
import { mockApi, now, type MockApi } from './mock-api';

// The dashboard's New dialog and a card's Add to calendar (ARCHITECTURE.md
// §5), against the Vite dev server with the API mocked in the browser.

// Local times below are Athens time (UTC+3 in October), so the invite's UTC
// times are three hours earlier.
test.use({ timezoneId: 'Europe/Athens' });

const readable = /^[a-z]{3}-[a-z]{4}-[a-z]{3}$/;
const slugRules = 'Use 3–64 lowercase letters, numbers, and single hyphens.';
const takenMessage = 'That room link is already in use.';

async function openDialog(page: Page, api?: MockApi) {
  if (!api) await mockApi(page);
  await page.goto('/');
  await page.getByRole('button', { name: 'New', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'New room' });
  await expect(dialog).toBeVisible();
  return {
    dialog,
    name: dialog.getByRole('textbox', { name: 'Name' }),
    slug: dialog.getByRole('textbox', { name: 'Link' }),
    create: dialog.getByRole('button', { name: 'Create', exact: true }),
    createAndJoin: dialog.getByRole('button', { name: 'Create & join' })
  };
}

function createBodies(page: Page): CreateRoomRequest[] {
  const bodies: CreateRoomRequest[] = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === RoomsPath) {
      bodies.push(request.postDataJSON() as CreateRoomRequest);
    }
  });
  return bodies;
}

/** The downloaded file's text, CRLFs intact, and its unfolded lines. */
async function invite(download: Download): Promise<{ text: string; lines: string[] }> {
  const text = await readFile((await download.path())!, 'utf8');
  return { text, lines: text.replace(/\r\n /g, '').split('\r\n') };
}

test('creates a room with the suggested link and no name', async ({ page }) => {
  const api = await mockApi(page);
  const bodies = createBodies(page);
  const { dialog, name, slug, create } = await openDialog(page, api);

  await expect(name).toBeFocused();
  const suggested = await slug.inputValue();
  expect(suggested).toMatch(readable);
  // A blank name will be the link, and the field says so.
  await expect(name).toHaveAttribute('placeholder', suggested);

  await create.click();
  await expect(dialog).toBeHidden();
  expect(bodies).toEqual([{ slug: suggested }]);
  const card = page.locator(`[data-testid="room-card"][data-slug="${suggested}"]`);
  await expect(card).toContainText(suggested);
  await expect(page.getByTestId('room-card').first()).toHaveAttribute('data-slug', suggested);
});

test('shuffle draws another suggestion', async ({ page }) => {
  const { dialog, slug } = await openDialog(page);
  const first = await slug.inputValue();
  await dialog.getByRole('button', { name: 'Suggest another link' }).click();
  const second = await slug.inputValue();
  expect(second).toMatch(readable);
  expect(second).not.toBe(first);
});

test('creates a room with a custom name and link', async ({ page }) => {
  const api = await mockApi(page);
  const bodies = createBodies(page);
  const { dialog, name, slug, create } = await openDialog(page, api);

  await name.fill('  Design review ');
  await slug.fill(' Design-Review ');
  await create.click();

  await expect(dialog).toBeHidden();
  expect(bodies).toEqual([{ name: 'Design review', slug: 'design-review' }]);
  const card = page.locator('[data-testid="room-card"][data-slug="design-review"]');
  await expect(card).toContainText('Design review');
  await expect(card.getByRole('link', { name: 'Join meeting' })).toHaveAttribute(
    'href',
    '/m/design-review'
  );
});

test('a link that breaks the rules is caught before it is sent', async ({ page }) => {
  const api = await mockApi(page);
  const { dialog, slug, create } = await openDialog(page, api);

  await slug.fill('a--b');
  await slug.blur();
  await expect(dialog.getByText(slugRules)).toBeVisible();
  await expect(slug).toHaveAttribute('aria-invalid', 'true');
  await create.click();
  expect(api.count('POST', RoomsPath)).toBe(0);

  await slug.fill('a-b');
  await expect(dialog.getByText(slugRules)).toBeHidden();
  await expect(slug).toHaveAttribute('aria-invalid', 'false');
});

test('a taken link the user typed says so under the field', async ({ page }) => {
  const api = await mockApi(page);
  const { dialog, slug, create } = await openDialog(page, api);

  await slug.fill('standup');
  await create.click();

  await expect(dialog.getByText(takenMessage)).toBeVisible();
  await expect(slug).toHaveAttribute('aria-invalid', 'true');
  await expect(slug).toHaveValue('standup');
  await expect(dialog).toBeVisible();
  expect(api.count('POST', RoomsPath)).toBe(1);

  // Typing again clears the server's answer.
  await slug.fill('standup-2');
  await expect(dialog.getByText(takenMessage)).toBeHidden();
  await create.click();
  await expect(dialog).toBeHidden();
  await expect(page.locator('[data-testid="room-card"][data-slug="standup-2"]')).toBeVisible();
});

test('an untouched suggestion that is taken is redrawn silently', async ({ page }) => {
  const api = await mockApi(page);
  const bodies = createBodies(page);
  const { dialog, slug, create } = await openDialog(page, api);

  // Someone else takes the suggestion while the dialog is open.
  const suggested = await slug.inputValue();
  api.state.rooms.push({ ...api.state.rooms[0], id: 'r-other', slug: suggested, name: 'Other' });

  await create.click();
  await expect(dialog).toBeHidden();
  expect(bodies).toHaveLength(2);
  expect(bodies[0].slug).toBe(suggested);
  expect(bodies[1].slug).toMatch(readable);
  expect(bodies[1].slug).not.toBe(suggested);
  await expect(
    page.locator(`[data-testid="room-card"][data-slug="${bodies[1].slug}"]`)
  ).toBeVisible();
});

test('redrawing gives up after three tries and says why', async ({ page }) => {
  const api = await mockApi(page);
  await page.route(
    (url) => url.pathname === RoomsPath,
    (route) =>
      route.request().method() === 'POST'
        ? route.fulfill({
            status: 409,
            contentType: 'application/json',
            body: JSON.stringify({ error: takenMessage })
          })
        : route.fallback()
  );
  const { dialog, create } = await openDialog(page, api);
  const posts: string[] = [];
  page.on('request', (request) => {
    if (request.method() === 'POST') posts.push(request.url());
  });

  await create.click();
  await expect(dialog.getByText(takenMessage)).toBeVisible();
  expect(posts).toHaveLength(3);
});

test('Create & join opens the meeting', async ({ page }) => {
  const { name, slug } = await openDialog(page);
  await slug.fill('quick-sync');
  await name.fill('Quick sync');
  // Enter submits the primary action.
  await name.press('Enter');
  await page.waitForURL('**/m/quick-sync');
});

test('Escape closes the dialog and gives focus back to New', async ({ page }) => {
  const { dialog } = await openDialog(page);
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(page.getByRole('button', { name: 'New', exact: true })).toBeFocused();
});

test('with times set, creating downloads the invite', async ({ page }) => {
  await page.clock.setFixedTime(new Date('2026-10-05T10:12:00+03:00'));
  const api = await mockApi(page);
  const { dialog, name, slug, create } = await openDialog(page, api);

  await name.fill('Retro, Q4');
  await slug.fill('retro-q4');
  await dialog.getByRole('checkbox', { name: 'Add to calendar' }).check();
  const date = dialog.getByLabel('Date');
  const start = dialog.getByLabel('Starts');
  const end = dialog.getByLabel('Ends');
  await expect(date).toHaveValue('2026-10-05');
  await expect(start).toHaveValue('10:30');
  await expect(end).toHaveValue('11:00');

  // An end before the start is caught here and nothing is created.
  await end.fill('10:00');
  await create.click();
  await expect(dialog.getByText('End the meeting after it starts.')).toBeVisible();
  expect(api.count('POST', RoomsPath)).toBe(0);

  await end.fill('11:15');
  const [download] = await Promise.all([page.waitForEvent('download'), create.click()]);
  expect(download.suggestedFilename()).toBe('retro-q4.ics');
  const { text, lines } = await invite(download);
  expect(text.replace(/\r\n/g, '')).not.toMatch(/[\r\n]/);
  const url = new URL('/m/retro-q4', page.url()).href;
  expect(lines).toContain('SUMMARY:Retro\\, Q4');
  expect(lines).toContain(`URL:${url}`);
  expect(lines).toContain(`LOCATION:${url}`);
  expect(lines).toContain('DTSTART:20261005T073000Z');
  expect(lines).toContain('DTEND:20261005T081500Z');
  expect(lines).toContain(`UID:retro-q4-20261005T073000Z@${new URL(page.url()).host}`);
  await expect(dialog).toBeHidden();
  await expect(page.locator('[data-testid="room-card"][data-slug="retro-q4"]')).toBeVisible();
});

test('without times, creating downloads nothing', async ({ page }) => {
  const { dialog, create } = await openDialog(page);
  let downloads = 0;
  page.on('download', () => downloads++);
  await create.click();
  await expect(dialog).toBeHidden();
  expect(downloads).toBe(0);
});

test('a room card adds the room to a calendar', async ({ page }) => {
  await page.clock.setFixedTime(new Date('2026-10-05T16:40:00+03:00'));
  const api = await mockApi(page);
  api.state.rooms[0].last_active_at = now;
  await page.goto('/');

  await page.getByRole('button', { name: 'Add Standup to calendar' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add to calendar' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel('Date')).toBeFocused();
  await expect(dialog.getByLabel('Starts')).toHaveValue('17:00');
  await expect(dialog.getByLabel('Ends')).toHaveValue('17:30');

  await dialog.getByLabel('Date').fill('2026-10-07');
  await dialog.getByLabel('Starts').fill('09:00');
  await dialog.getByLabel('Ends').fill('09:45');
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    dialog.getByRole('button', { name: 'Download .ics' }).click()
  ]);
  expect(download.suggestedFilename()).toBe('standup.ics');
  const { lines } = await invite(download);
  expect(lines).toContain('SUMMARY:Standup');
  expect(lines).toContain(`URL:${new URL('/m/standup', page.url()).href}`);
  expect(lines).toContain('DTSTART:20261007T060000Z');
  expect(lines).toContain('DTEND:20261007T064500Z');
  await expect(dialog).toBeHidden();
  // Nothing about the invite reaches the server.
  expect(api.calls.filter((call) => call.method !== 'GET')).toEqual([]);
});

test('a cleared link asks the server to draw one', async ({ page }) => {
  const api = await mockApi(page);
  const bodies = createBodies(page);
  const { dialog, name, slug, create } = await openDialog(page, api);

  await slug.fill('');
  await slug.blur();
  // Blank is not an error: the field says a random link is drawn.
  await expect(slug).toHaveAttribute('aria-invalid', 'false');
  await expect(slug).toHaveAttribute('placeholder', 'random');
  await expect(name).toHaveAttribute('placeholder', 'Same as the link');

  await create.click();
  await expect(dialog).toBeHidden();
  expect(bodies).toEqual([{}]);
  const created = api.state.rooms[0];
  expect(created.slug).toMatch(readable);
  await expect(
    page.locator(`[data-testid="room-card"][data-slug="${created.slug}"]`)
  ).toBeVisible();
});

test('a 60-character Greek name is accepted', async ({ page }) => {
  const api = await mockApi(page);
  const bodies = createBodies(page);
  const { dialog, name, slug, create } = await openDialog(page, api);

  const greek = 'Εβδομαδιαία σύσκεψη ομάδας προϊόντος και σχεδιασμού για εσάς';
  expect([...greek].length).toBe(60);
  await name.fill(greek);
  await slug.fill('evdomadiaia');
  await create.click();

  await expect(dialog).toBeHidden();
  expect(bodies).toEqual([{ name: greek, slug: 'evdomadiaia' }]);
  await expect(page.locator('[data-testid="room-card"][data-slug="evdomadiaia"]')).toContainText(
    greek
  );
});

test('the dialog stays up while a create is in flight', async ({ page }) => {
  const api = await mockApi(page);
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  let answered = 0;
  await page.route(
    (url) => url.pathname === RoomsPath,
    async (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      await held;
      answered++;
      return route.fallback();
    }
  );
  const { dialog, slug, create, createAndJoin } = await openDialog(page, api);
  const suggested = await slug.inputValue();

  await create.click();
  await expect(create).toBeDisabled();
  await expect(createAndJoin).toBeDisabled();

  // Escape (twice: Chrome stops honouring a refused cancel without other
  // input), the backdrop and the close button all leave it open.
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  await expect(dialog).toBeVisible();
  const close = dialog.getByRole('button', { name: 'Close' });
  await expect(close).toBeDisabled();
  await page.mouse.click(4, 4);
  await expect(dialog).toBeVisible();

  release();
  await expect(dialog).toBeHidden();
  expect(answered).toBe(1);
  expect(api.count('POST', RoomsPath)).toBe(1);
  await expect(page.locator(`[data-testid="room-card"][data-slug="${suggested}"]`)).toHaveCount(1);

  // Once it has settled the dialog opens fresh and closes normally.
  await page.getByRole('button', { name: 'New', exact: true }).click();
  await expect(dialog).toBeVisible();
  await expect(create).toBeEnabled();
  await expect(close).toBeEnabled();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  expect(api.count('POST', RoomsPath)).toBe(1);
});

test('a missing date marks the date field', async ({ page }) => {
  const api = await mockApi(page);
  const { dialog, create } = await openDialog(page, api);
  await dialog.getByRole('checkbox', { name: 'Add to calendar' }).check();
  const date = dialog.getByLabel('Date');
  await date.fill('');
  await create.click();

  const message = dialog.getByText('Pick a date, a start time and an end time.');
  await expect(message).toBeVisible();
  const messageId = await message.getAttribute('id');
  await expect(date).toHaveAttribute('aria-invalid', 'true');
  await expect(date).toHaveAttribute('aria-describedby', messageId!);
  await expect(dialog.getByLabel('Starts')).toHaveAttribute('aria-invalid', 'false');
  await expect(dialog.getByLabel('Ends')).toHaveAttribute('aria-invalid', 'false');
  expect(api.count('POST', RoomsPath)).toBe(0);
});

test('Create & join with times downloads the invite and opens the meeting', async ({ page }) => {
  await page.clock.setFixedTime(new Date('2026-10-05T10:12:00+03:00'));
  const api = await mockApi(page);
  const { dialog, slug, createAndJoin } = await openDialog(page, api);
  await slug.fill('kickoff');
  await dialog.getByRole('checkbox', { name: 'Add to calendar' }).check();

  const [download] = await Promise.all([page.waitForEvent('download'), createAndJoin.click()]);
  expect(download.suggestedFilename()).toBe('kickoff.ics');
  const { lines } = await invite(download);
  expect(lines).toContain('DTSTART:20261005T073000Z');
  expect(lines).toContain('DTEND:20261005T080000Z');
  await page.waitForURL('**/m/kickoff');
});

test('a create the server never answers ends on its own, and the dialog can close', async ({
  page
}) => {
  await page.clock.install();
  const api = await mockApi(page);
  await page.route(
    (url) => url.pathname === RoomsPath,
    (route) =>
      route.request().method() === 'POST' ? new Promise<void>(() => {}) : route.fallback()
  );
  const { dialog, create } = await openDialog(page, api);
  await create.click();
  await expect(create).toBeDisabled();
  await page.clock.fastForward(15_000);
  await expect(dialog.getByText('The server took too long to answer.')).toBeVisible();
  await expect(create).toBeEnabled();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
});
