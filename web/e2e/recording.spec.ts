import { test, expect, chromium, type BrowserContext } from '@playwright/test';
import { createRoomAndGetSlug, dexLogin, joinAsGuestThroughLobby } from './helpers';

test('host records a two-participant room and manages the completed MP4', async () => {
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
		const hostContext = await newContext();
		const host = await hostContext.newPage();
		await dexLogin(host);
		const slug = await createRoomAndGetSlug(host, `Recording e2e ${Date.now()}`);
		await host.goto(`/m/${slug}`);
		await host.getByRole('button', { name: 'Join room' }).click();
		await expect(host.getByRole('button', { name: 'Start recording' })).toBeVisible({
			timeout: 20_000
		});

		const guest = await (await newContext()).newPage();
		await joinAsGuestThroughLobby(guest, host, slug, 'Recorded guest');
		await expect(host.locator(`video[aria-label="Recorded guest's video"]`)).toBeVisible({
			timeout: 20_000
		});
		await expect(guest.locator('video[aria-label$="video"]')).toHaveCount(2, {
			timeout: 20_000
		});

		await host.getByRole('button', { name: 'Start recording' }).click();
		const startedResponsePromise = host.waitForResponse(
			(response) =>
				response.request().method() === 'POST' &&
				response.url().endsWith(`/api/rooms/${slug}/recording/start`),
			{ timeout: 20_000 }
		);
		await host.getByRole('button', { name: 'Record?' }).click();
		const startedResponse = await startedResponsePromise;
		if (startedResponse.status() >= 500) {
			test.skip(true, 'LiveKit Egress is unhealthy or unavailable in this environment.');
		}
		expect(startedResponse.status()).toBe(201);

		// Egress cold-starts Chrome and loads the template through Vite's dev
		// transform; only the egress_started webhook (status 'recording') proves
		// capture actually began. Stopping earlier aborts with "Start signal
		// not received".
		await expect
			.poll(
				async () => {
					const list = await host.request.get(`/api/rooms/${slug}/recordings`);
					const items = (await list.json()) as { status: string }[];
					return items[0]?.status ?? 'none';
				},
				{ timeout: 90_000 }
			)
			.toBe('recording');

		for (const participant of [host, guest]) {
			await expect(participant.getByTestId('recording-chip')).toHaveText('REC', {
				timeout: 20_000
			});
			await expect(participant.getByTestId('hairline')).toHaveAttribute(
				'data-state',
				'recording',
				{ timeout: 20_000 }
			);
		}

		await host.waitForTimeout(8_000);
		const stoppedResponsePromise = host.waitForResponse(
			(response) =>
				response.request().method() === 'POST' &&
				response.url().endsWith(`/api/rooms/${slug}/recording/stop`),
			{ timeout: 20_000 }
		);
		await host.getByRole('button', { name: 'Stop recording' }).click();
		expect((await stoppedResponsePromise).status()).toBe(200);
		for (const participant of [host, guest]) {
			await expect(participant.getByTestId('recording-chip')).toHaveCount(0, {
				timeout: 20_000
			});
			await expect(participant.getByTestId('hairline')).toHaveAttribute(
				'data-state',
				'connected',
				{ timeout: 20_000 }
			);
		}

		let completed: any;
		await expect
			.poll(
				async () => {
					const response = await hostContext.request.get(`/api/rooms/${slug}/recordings`);
					expect(response.ok()).toBe(true);
					const recordings = (await response.json()) as any[];
					completed = recordings[0];
					return completed?.status;
				},
				{ timeout: 90_000, intervals: [1_000, 2_000, 3_000] }
			)
			.toBe('completed');

		expect(completed.duration_s).toBeGreaterThanOrEqual(5);
		const download = await hostContext.request.get(
			`/api/recordings/${encodeURIComponent(completed.id)}/download`
		);
		expect(download.ok(), `status=${download.status()} body=${(await download.text()).slice(0, 300)}`).toBe(true);
		expect(Number(download.headers()['content-length'])).toBeGreaterThan(100_000);

		const deleted = await hostContext.request.delete(
			`/api/recordings/${encodeURIComponent(completed.id)}`,
			{ headers: { 'X-Klisi-Csrf': '1' } }
		);
		expect(deleted.status()).toBe(204);
		await expect
			.poll(async () => {
				const response = await hostContext.request.get(`/api/rooms/${slug}/recordings`);
				return ((await response.json()) as any[]).length;
			})
			.toBe(0);
	} finally {
		await Promise.all(contexts.map((context) => context.close()));
		await browser.close();
	}
});
