import { test, expect, chromium, type Browser } from '@playwright/test';

test('microphone control updates UI and the local publication', async () => {
	const browser: Browser = await chromium.launch({
		args: [
			'--use-fake-ui-for-media-stream',
			'--use-fake-device-for-media-stream',
			'--autoplay-policy=no-user-gesture-required'
		]
	});

	try {
		const context = await browser.newContext({ permissions: ['camera', 'microphone'] });
		const page = await context.newPage();
		await page.goto('/');
		await page.fill('input[name="room"]', `controls-e2e-${Date.now()}`);
		await page.fill('input[name="name"]', 'alice');
		await page.click('button[type="submit"]');

		const microphone = page.getByRole('button', { name: 'Mute microphone' });
		await expect(microphone).toBeVisible({ timeout: 20_000 });
		await expect(microphone).toHaveAttribute('aria-pressed', 'true');
		await microphone.click();
		await expect(page.getByRole('button', { name: 'Unmute microphone' })).toHaveAttribute(
			'aria-pressed',
			'false'
		);

		await expect
			.poll(
				() =>
					page.evaluate(() => {
						const room = (
							window as Window & {
								__klisiRoom?: {
									localParticipant: {
										audioTrackPublications: Map<string, { source: string; isMuted: boolean }>;
									};
								};
							}
						).__klisiRoom;
						const publication = room
							? [...room.localParticipant.audioTrackPublications.values()].find(
									(candidate) => candidate.source === 'microphone'
								)
							: undefined;
						return publication?.isMuted ?? false;
					}),
				{ timeout: 10_000 }
			)
			.toBe(true);
	} finally {
		await browser.close();
	}
});
