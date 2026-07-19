import { test, expect, chromium, type Browser, type Page } from '@playwright/test';

test('three participants see every camera tile', async () => {
	const browser: Browser = await chromium.launch({
		args: [
			'--use-fake-ui-for-media-stream',
			'--use-fake-device-for-media-stream',
			'--autoplay-policy=no-user-gesture-required'
		]
	});
	const room = `three-e2e-${Date.now()}`;

	try {
		const join = async (name: string): Promise<Page> => {
			const context = await browser.newContext({ permissions: ['camera', 'microphone'] });
			const page = await context.newPage();
			await page.goto('/dev');
			await page.fill('input[name="room"]', room);
			await page.fill('input[name="name"]', name);
			await page.click('button[type="submit"]');
			return page;
		};

		const participants = await Promise.all([join('alice'), join('bob'), join('carol')]);
		const names = ['alice', 'bob', 'carol'];

		for (const page of participants) {
			await expect(page.getByTestId('participant-tile')).toHaveCount(3, { timeout: 20_000 });
			for (const name of names) {
				const video = page.locator(`video[aria-label="${name}'s video"]`);
				await expect(video).toBeVisible({ timeout: 20_000 });
			}
		}
	} finally {
		await browser.close();
	}
});
