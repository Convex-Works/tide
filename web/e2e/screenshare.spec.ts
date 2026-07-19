import { test, expect, chromium, type Browser, type Page } from '@playwright/test';

test('a screen share is promoted to the focus pane for remote participants', async () => {
	const browser: Browser = await chromium.launch({
		args: [
			'--use-fake-ui-for-media-stream',
			'--use-fake-device-for-media-stream',
			'--autoplay-policy=no-user-gesture-required',
			'--auto-select-desktop-capture-source=Entire'
		]
	});
	const room = `screenshare-e2e-${Date.now()}`;

	try {
		const join = async (name: string): Promise<Page> => {
			const context = await browser.newContext({ permissions: ['camera', 'microphone'] });
			const page = await context.newPage();
			await page.goto('/dev');
			await page.fill('input[name="room"]', room);
			await page.fill('input[name="name"]', name);
			await page.click('button[type="submit"]');
			await expect(page.getByRole('button', { name: 'Share screen' })).toBeVisible({
				timeout: 20_000
			});
			return page;
		};

		const alice = await join('alice');
		const bob = await join('bob');
		await expect(alice.locator(`video[aria-label="bob's video"]`)).toBeVisible({
			timeout: 20_000
		});

		await alice.getByRole('button', { name: 'Share screen' }).click();
		await expect(bob.getByTestId('focus-pane')).toBeVisible({ timeout: 20_000 });
		await expect(bob.locator(`video[aria-label="alice's screen share"]`)).toBeVisible();
	} finally {
		await browser.close();
	}
});
