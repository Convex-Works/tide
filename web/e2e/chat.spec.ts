import { test, expect, chromium, type BrowserContext } from '@playwright/test';
import { createRoomAndGetSlug, dexLogin, joinAsGuestThroughLobby } from './helpers';

test('chat is bidirectional, counts unread messages, and has no rejoin history', async () => {
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
		const slug = await createRoomAndGetSlug(host, `Chat e2e ${Date.now()}`);
		await host.goto(`/m/${slug}`);
		await host.getByRole('button', { name: 'Join room' }).click();
		await expect(host.getByRole('button', { name: 'Share screen' })).toBeVisible({
			timeout: 20_000
		});

		const guest = await (await newContext()).newPage();
		await joinAsGuestThroughLobby(guest, host, slug, 'Chat guest');

		await host.getByRole('button', { name: 'Open chat' }).click();
		const hostChat = host.getByRole('complementary', { name: 'Chat' });
		await hostChat.getByRole('textbox', { name: 'Chat message' }).fill('Hello from host');
		await hostChat.getByRole('textbox', { name: 'Chat message' }).press('Enter');

		const guestChatButton = guest.getByRole('button', { name: 'Open chat' });
		await expect(guestChatButton.locator('.badge')).toHaveText('1', { timeout: 20_000 });
		await guestChatButton.click();
		const guestChat = guest.getByRole('complementary', { name: 'Chat' });
		await expect(guestChat.getByText('Hello from host')).toBeVisible();
		await guestChat.getByRole('textbox', { name: 'Chat message' }).fill('Hello from guest');
		await guestChat.getByRole('textbox', { name: 'Chat message' }).press('Enter');
		await expect(hostChat.getByText('Hello from guest')).toBeVisible({ timeout: 20_000 });

		await guest.reload();
		await joinAsGuestThroughLobby(guest, host, slug, 'Chat guest');
		await guest.getByRole('button', { name: 'Open chat' }).click();
		const freshChat = guest.getByRole('complementary', { name: 'Chat' });
		await expect(freshChat.getByText('Messages appear here for this meeting only.')).toBeVisible();
		await expect(freshChat.getByText('Hello from host')).toHaveCount(0);
		await expect(freshChat.getByText('Hello from guest')).toHaveCount(0);
	} finally {
		await Promise.all(contexts.map((context) => context.close()));
		await browser.close();
	}
});
