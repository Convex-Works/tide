import { test, chromium } from '@playwright/test';

// One-off design review captures for Phase 2 surfaces.
test('capture dashboard and lobby states', async () => {
	test.setTimeout(120_000);
	const browser = await chromium.launch({
		args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream']
	});
	const host = await browser.newContext({
		permissions: ['camera', 'microphone'],
		viewport: { width: 1280, height: 800 }
	});
	const page = await host.newPage();
	await page.goto('/');
	await page.waitForTimeout(800);
	await page.screenshot({ path: 'shots/signin.png' });

	await page.getByRole('button', { name: /continue with sso/i }).click();
	await page.fill('input[name="login"]', 'host@klisi.dev');
	await page.fill('input[name="password"]', 'klisi-dev');
	await page.click('button[type="submit"]');
	await page.waitForURL('http://localhost:5173/**', { timeout: 20_000 });
	await page.waitForTimeout(800);
	await page.screenshot({ path: 'shots/dashboard.png' });

	const guest = await browser.newContext({ viewport: { width: 1280, height: 800 } });
	const slugChip = page.locator('[data-testid="room-slug"], .slug').first();
	const slug = (await slugChip.textContent())?.trim();
	if (slug) {
		const gp = await guest.newPage();
		await gp.goto(`/m/${slug}`);
		await gp.waitForTimeout(1200);
		await gp.screenshot({ path: 'shots/prejoin-slug.png' });
	}
	await browser.close();
});
