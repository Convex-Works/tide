import { test, expect, chromium, type BrowserContext } from '@playwright/test';
import { createRoomAndGetSlug, dexLogin, joinAsGuestThroughLobby } from './helpers';

test('the host mutes and removes a guest who can request admission again', async () => {
  test.setTimeout(150_000);
  const browser = await chromium.launch({
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      '--autoplay-policy=no-user-gesture-required'
    ]
  });
  const contexts: BrowserContext[] = [];
  const newContext = async (): Promise<BrowserContext> => {
    const context = await browser.newContext({
      baseURL: 'http://localhost:5173',
      permissions: ['camera', 'microphone']
    });
    contexts.push(context);
    return context;
  };

  try {
    const host = await (await newContext()).newPage();
    await dexLogin(host);
    const slug = await createRoomAndGetSlug(host, `Moderation e2e ${Date.now()}`);
    await host.goto(`/m/${slug}`);
    await host.getByRole('button', { name: 'Join room' }).click();
    await expect(host.getByRole('button', { name: 'Share screen' })).toBeVisible({
      timeout: 20_000
    });

    const guest = await (await newContext()).newPage();
    await joinAsGuestThroughLobby(guest, host, slug, 'Moderated guest');

    const openPeople = host.getByRole('button', { name: 'Open people' });
    if (await openPeople.isVisible()) await openPeople.click();
    const people = host.getByRole('complementary', { name: 'People' });
    const guestRow = people.locator('.participant-row').filter({ hasText: 'Moderated guest' });
    await expect(guestRow).toBeVisible({ timeout: 20_000 });
    await guestRow.hover();
    await guestRow.getByRole('button', { name: 'Mute' }).click();

    await expect(guestRow.getByLabel('Microphone off')).toBeVisible({ timeout: 20_000 });
    await expect(guest.getByRole('button', { name: 'Unmute microphone' })).toBeVisible({
      timeout: 20_000
    });

    await guestRow.hover();
    await guestRow.getByRole('button', { name: 'Remove', exact: true }).click();
    await guestRow.getByRole('button', { name: 'Remove?' }).click();
    await expect(guest.getByText('You were removed from the meeting.')).toBeVisible({
      timeout: 20_000
    });
    await expect(
      people.locator('.participant-row').filter({ hasText: 'Moderated guest' })
    ).toHaveCount(0, { timeout: 20_000 });

    await guest.getByRole('button', { name: 'Request to rejoin' }).click();
    await guest.getByRole('button', { name: 'Join room' }).click();
    await expect(guest.getByText('Waiting for the host to let you in.')).toBeVisible({
      timeout: 20_000
    });
    await expect(people.getByText('Moderated guest', { exact: true })).toBeVisible({
      timeout: 20_000
    });
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
    await browser.close();
  }
});
