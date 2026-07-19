import { test, expect, chromium, type BrowserContext, type Page } from '@playwright/test';

test('a host admits one guest and denies another through the lobby', async () => {
	test.setTimeout(120_000);
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

	const enterLobby = async (page: Page, origin: string, slug: string, name: string) => {
		await page.goto(`${origin}/m/${slug}`);
		await page.fill('input[name="name"]', name);
		await page.getByRole('button', { name: 'Join room' }).click();
		await expect(page.getByText('Waiting for the host to let you in.')).toBeVisible({
			timeout: 20_000
		});
	};

	try {
		const hostContext = await newContext();
		const host = await hostContext.newPage();
		await host.goto('/');
		await host.getByRole('button', { name: 'Continue with SSO' }).click();
		await host.locator('input[name="login"]').fill('host@klisi.dev');
		await host.locator('input[name="password"]').fill('klisi-dev');
		await host.locator('button[type="submit"], input[type="submit"]').click();
		await host.waitForURL((url) => url.pathname === '/', { timeout: 30_000 });

		const roomName = `Lobby e2e ${Date.now()}`;
		await host.fill('input[name="room-name"]', roomName);
		await host.getByRole('button', { name: 'New room' }).click();
		const roomRow = host.locator('article.room-row').filter({ hasText: roomName });
		await expect(roomRow).toBeVisible();
		const slug = (await roomRow.locator('.slug').textContent())?.trim();
		expect(slug).toBeTruthy();
		await roomRow.locator('a.room-link').click();
		await host.getByRole('button', { name: 'Join room' }).click();
		await expect(host.getByRole('button', { name: 'Share screen' })).toBeVisible({
			timeout: 20_000
		});

		const appOrigin = new URL(host.url()).origin;
		const visitorContext = await newContext();
		const visitor = await visitorContext.newPage();
		await enterLobby(visitor, appOrigin, slug!, 'Visitor');

		const lobby = host.getByRole('complementary', { name: 'Lobby' });
		await expect(lobby.getByText('Visitor')).toBeVisible({ timeout: 20_000 });
		await lobby.getByRole('button', { name: 'Admit' }).click();
		await expect(visitor.getByRole('button', { name: 'Share screen' })).toBeVisible({
			timeout: 20_000
		});
		await expect(
			host.getByTestId('participant-tile').filter({ hasText: 'Visitor' })
		).toBeVisible({ timeout: 20_000 });

		const deniedContext = await newContext();
		const denied = await deniedContext.newPage();
		await enterLobby(denied, appOrigin, slug!, 'Second visitor');
		await expect(lobby.getByText('Second visitor')).toBeVisible({ timeout: 20_000 });
		await lobby.getByRole('button', { name: 'Deny' }).click();
		await expect(denied.getByText('The host did not let you in.')).toBeVisible({
			timeout: 20_000
		});
	} finally {
		await Promise.all(contexts.map((context) => context.close()));
		await browser.close();
	}
});
