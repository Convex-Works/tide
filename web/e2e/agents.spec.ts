import { expect, test, type Page } from '@playwright/test';
import { fakeSfu, fakeSfuLaunchArgs } from './fake-sfu';
import { defaultState, fakeSfuURL, mockApi, type ApiState } from './mock-api';
import { AgentSessionPath, type AgentSession } from '../src/lib/api/types.gen';

// A program in a meeting (docs/MEETING-AGENTS.md) as the people there see it.
// The tag and the hairline are all that tell a guest something is listening,
// so they are not polish. The API is mocked and the SFU is fake-sfu.ts, so
// this runs against the Vite dev server alone.

test.use({
  launchOptions: { args: fakeSfuLaunchArgs },
  permissions: ['clipboard-read', 'clipboard-write']
});

function guest(): ApiState {
  const state = defaultState();
  state.signedIn = false;
  state.rooms[0] = { ...state.rooms[0], lobby_enabled: false };
  return state;
}

function administrator(): ApiState {
  return { ...defaultState(), administrator: true };
}

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

const agentTile = (page: Page) =>
  page.locator('[data-testid="participant-tile"][data-identity="agent:0001"]');

test('a guest sees an agent on its tile, in the list and on the hairline, for as long as it stays', async ({
  page
}) => {
  await mockApi(page, guest());
  const sfu = await fakeSfu(page);
  await enter(page, 'Grace Guest');

  await expect(page.getByTestId('hairline')).toHaveAttribute('data-state', 'connected');
  await expect(page.getByTestId('hairline-agent')).toHaveCount(0);
  await expect(page.getByTestId('agent-chip')).toHaveCount(0);

  sfu.addAgent('agent:0001', 'Notes');

  const tile = agentTile(page);
  await expect(tile.locator('.name-label')).toContainText('Notes');
  await expect(tile.locator('.name-label')).toContainText('Agent');
  await expect(page.getByTestId('hairline-agent')).toBeVisible();
  await expect(page.getByTestId('agent-chip')).toHaveText('AGENT');
  // Alone it is the whole line, in the one color no other state uses.
  const width = page.viewportSize()?.width ?? 0;
  const line = await page.getByTestId('hairline-agent').boundingBox();
  expect(line).toMatchObject({ x: 0, width });
  await expect(page.getByTestId('hairline-agent')).toHaveCSS(
    'background-color',
    'rgb(79, 163, 111)'
  );

  await page.getByRole('button', { name: 'People', exact: true }).click();
  const row = page.locator('.participant-row[data-identity="agent:0001"]');
  await expect(row).toContainText('Notes');
  await expect(row.locator('.role')).toHaveText('Agent');
  // The guest's own row says nothing of the kind.
  await expect(
    page.locator('.participant-row').filter({ hasText: 'Grace Guest' })
  ).not.toContainText('Agent');

  sfu.removeAgent('agent:0001');
  await expect(tile).toHaveCount(0);
  await expect(page.getByTestId('hairline-agent')).toHaveCount(0);
  await expect(page.getByTestId('agent-chip')).toHaveCount(0);
});

test('recording and an agent show together: neither hides the other', async ({ page }) => {
  await mockApi(page, guest());
  const sfu = await fakeSfu(page);
  await enter(page, 'Grace Guest');

  sfu.setRoomMetadata('{"recording":true}');
  sfu.addAgent('agent:0001', 'Notes');

  await expect(page.getByTestId('hairline')).toHaveAttribute('data-state', 'recording');
  await expect(page.getByTestId('recording-chip')).toBeVisible();
  await expect(page.getByTestId('agent-chip')).toBeVisible();
  const width = page.viewportSize()?.width ?? 0;
  await expect
    .poll(async () => (await page.getByTestId('hairline-agent').boundingBox())?.x)
    .toBe(width / 2);

  // The recording's pulse doesn't reach the agent's half.
  await expect(page.getByTestId('hairline-agent')).toHaveCSS('animation-name', 'none');
});

test('leaving the meeting takes the agent off the hairline', async ({ page }) => {
  await mockApi(page, guest());
  const sfu = await fakeSfu(page);
  await enter(page, 'Grace Guest');
  sfu.addAgent('agent:0001', 'Notes');
  await expect(page.getByTestId('hairline-agent')).toBeVisible();

  await page.getByRole('button', { name: 'Leave room' }).click();
  await expect(page.getByText('You left the meeting.')).toBeVisible();
  await expect(page.getByTestId('hairline-agent')).toHaveCount(0);
});

test('the owner copies an agent session from the meeting details', async ({ page }) => {
  const api = await mockApi(page);
  await fakeSfu(page);
  await enter(page);

  await page.getByRole('button', { name: 'Meeting details' }).click();
  const details = page.getByRole('dialog', { name: 'Meeting details' });
  await details.getByRole('button', { name: 'Copy agent session' }).click();
  await expect(details.getByRole('button', { name: 'Copied' })).toBeVisible();

  const copied = JSON.parse(await page.evaluate(() => navigator.clipboard.readText()));
  expect(copied).toEqual({
    ws_url: fakeSfuURL,
    token: expect.any(String),
    room: 'standup',
    identity: 'agent:0001',
    name: 'Agent',
    owner_identity: 'host:owner',
    expires_at: expect.any(Number)
  } satisfies Record<keyof AgentSession, unknown>);
  const call = api.calls.find(
    (item) => item.path === AgentSessionPath.replace('{slug}', 'standup')
  );
  expect(call?.csrf).toBe('1');
});

test('a clipboard that refuses leaves the session on screen, selected', async ({ page }) => {
  await page.addInitScript(() => {
    navigator.clipboard.writeText = () => Promise.reject(new Error('Not allowed.'));
    document.execCommand = () => false;
  });
  await mockApi(page);
  await fakeSfu(page);
  await enter(page);

  await page.getByRole('button', { name: 'Meeting details' }).click();
  const details = page.getByRole('dialog', { name: 'Meeting details' });
  await details.getByRole('button', { name: 'Copy agent session' }).click();
  await expect(details.getByRole('alert')).toHaveText(
    "Couldn't copy. The session is selected; copy it by hand."
  );
  await expect(details.locator('.session')).toContainText('"identity": "agent:0001"');
  expect(await page.evaluate(() => window.getSelection()?.toString())).toContain('agent:0001');

  // It doesn't stay around once the details close.
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'Meeting details' }).click();
  await expect(
    page.getByRole('dialog', { name: 'Meeting details' }).locator('.session')
  ).toHaveCount(0);
});

test('a refused session says why', async ({ page }) => {
  await mockApi(page);
  await fakeSfu(page);
  await enter(page);
  await page.route(`**${AgentSessionPath.replace('{slug}', 'standup')}`, (route) =>
    route.fulfill({
      status: 403,
      contentType: 'application/json',
      body: JSON.stringify({ error: "Only the room's owner can connect an agent." })
    })
  );

  await page.getByRole('button', { name: 'Meeting details' }).click();
  const details = page.getByRole('dialog', { name: 'Meeting details' });
  await details.getByRole('button', { name: 'Copy agent session' }).click();
  await expect(details.getByRole('alert')).toHaveText(
    "Only the room's owner can connect an agent."
  );
  await expect(details.locator('.session')).toHaveCount(0);
});

for (const [who, state, name] of [
  ['a guest', guest, 'Grace Guest'],
  ['an administrator who manages the room', administrator, undefined]
] as const) {
  test(`${who} is not offered an agent session`, async ({ page }) => {
    const api = await mockApi(page, state());
    await fakeSfu(page);
    await enter(page, name);

    await page.getByRole('button', { name: 'Meeting details' }).click();
    const details = page.getByRole('dialog', { name: 'Meeting details' });
    await expect(details.getByRole('button', { name: 'Copy link' })).toBeVisible();
    await expect(details.getByRole('button', { name: 'Copy agent session' })).toHaveCount(0);
    expect(api.count('POST', AgentSessionPath.replace('{slug}', 'standup'))).toBe(0);
  });
}
