import { test, chromium } from '@playwright/test';

// Screenshot capture for design review — not an assertion test.
test('capture pre-join and stage', async () => {
	const browser = await chromium.launch({
		args: [
			'--use-fake-ui-for-media-stream',
			'--use-fake-device-for-media-stream',
			'--autoplay-policy=no-user-gesture-required'
		]
	});
	const room = `shots-${Date.now()}`;
	const ctx = await browser.newContext({
		permissions: ['camera', 'microphone'],
		viewport: { width: 1280, height: 800 }
	});
	const page = await ctx.newPage();
	await page.goto('/');
	await page.waitForTimeout(1500);
	await page.screenshot({ path: 'shots/prejoin.png' });

	await page.fill('input[name="room"]', room);
	await page.fill('input[name="name"]', 'alice');
	await page.click('button[type="submit"]');
	await page.waitForTimeout(1500);

	const ctx2 = await browser.newContext({
		permissions: ['camera', 'microphone'],
		viewport: { width: 1280, height: 800 }
	});
	const page2 = await ctx2.newPage();
	await page2.goto('/');
	await page2.fill('input[name="room"]', room);
	await page2.fill('input[name="name"]', 'bob');
	await page2.click('button[type="submit"]');
	await page2.waitForTimeout(2500);
	await page.screenshot({ path: 'shots/stage-two.png' });
	await browser.close();
});
