import { test, expect, chromium } from '@playwright/test';
import { execFileSync } from 'node:child_process';

// Phase 1 acceptance: killing the network for ~10s shows the amber hairline,
// then the call resumes without a reload. Simulated by pausing the LiveKit
// container (SIGSTOP) — signal and media both freeze, then thaw.
test('connection loss shows reconnecting hairline and recovers', async () => {
	test.setTimeout(150_000);
	const browser = await chromium.launch({
		args: [
			'--use-fake-ui-for-media-stream',
			'--use-fake-device-for-media-stream',
			'--autoplay-policy=no-user-gesture-required'
		]
	});
	const context = await browser.newContext({ permissions: ['camera', 'microphone'] });
	const page = await context.newPage();
	await page.goto('/');
	await page.fill('input[name="room"]', `reconnect-${Date.now()}`);
	await page.fill('input[name="name"]', 'alice');
	await page.click('button[type="submit"]');

	const hairline = page.locator('[data-testid="hairline"]');
	await expect(hairline).toHaveAttribute('data-state', 'connected', { timeout: 15_000 });

	try {
		// stop (not pause): SIGSTOP leaves sockets half-open and undetectable;
		// stopping actually severs the signal WS like a real network loss.
		execFileSync('docker', ['stop', 'klisi-livekit-1']);
		await expect(hairline).toHaveAttribute('data-state', 'reconnecting', { timeout: 30_000 });
	} finally {
		execFileSync('docker', ['start', 'klisi-livekit-1']);
	}

	await expect(hairline).toHaveAttribute('data-state', 'connected', { timeout: 45_000 });
	await browser.close();
});
