import { expect, type Page } from '@playwright/test';

export async function dexLogin(page: Page): Promise<void> {
  await page.goto('/');
  await page.getByRole('button', { name: 'Continue with SSO' }).click();
  await page.locator('input[name="login"]').fill('host@klisi.dev');
  await page.locator('input[name="password"]').fill('klisi-dev');
  await page.locator('button[type="submit"], input[type="submit"]').click();
  await page.waitForURL((url) => url.pathname === '/', { timeout: 30_000 });
}

export async function createRoomAndGetSlug(
  page: Page,
  roomName = `Phase 3 e2e ${Date.now()}`
): Promise<string> {
  await page.fill('input[name="room-name"]', roomName);
  await page.getByRole('button', { name: 'New room' }).click();
  const roomRow = page.locator('article.room-row').filter({ hasText: roomName });
  await expect(roomRow).toBeVisible();
  const slug = (await roomRow.locator('.slug').textContent())?.trim();
  expect(slug).toBeTruthy();
  return slug!;
}

export async function joinAsGuestThroughLobby(
  guest: Page,
  host: Page,
  slug: string,
  name: string
): Promise<void> {
  await guest.goto(`/m/${slug}`);
  await guest.fill('input[name="name"]', name);
  await guest.getByRole('button', { name: 'Join room' }).click();
  await expect(guest.getByText('Waiting for the host to let you in.')).toBeVisible({
    timeout: 20_000
  });

  const people = host.getByRole('complementary', { name: 'People' });
  await expect(people.getByText(name, { exact: true })).toBeVisible({ timeout: 20_000 });
  const waiting = people.getByRole('region', { name: 'Lobby' });
  await waiting.getByRole('button', { name: 'Admit' }).click();
  await expect(guest.getByRole('button', { name: 'Share screen' })).toBeVisible({
    timeout: 20_000
  });
}
