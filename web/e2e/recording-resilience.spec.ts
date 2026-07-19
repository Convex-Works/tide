import { test, expect, chromium, request, type BrowserContext } from '@playwright/test';
import { createRoomAndGetSlug, dexLogin, joinAsGuestThroughLobby } from './helpers';

// Phase 4 acceptance: the recording is owned by the server, not any client.
// Killing the host's tab mid-recording must not stop it, and the recording
// must remain controllable (stop + finalize) through the API afterward.
test('recording survives the host tab dying', async () => {
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
		const slug = await createRoomAndGetSlug(host, `Resilience e2e ${Date.now()}`);
		await host.goto(`/m/${slug}`);
		await host.getByRole('button', { name: 'Join room' }).click();
		await expect(host.getByRole('button', { name: 'Start recording' })).toBeVisible({
			timeout: 20_000
		});

		const guest = await (await newContext()).newPage();
		await joinAsGuestThroughLobby(guest, host, slug, 'Survivor guest');

		// A session-bearing API context that outlives the host's tab.
		const api = await request.newContext({
			baseURL: 'http://localhost:5173',
			storageState: await hostContext.storageState()
		});
		const status = async (): Promise<string> => {
			const response = await api.get(`/api/rooms/${slug}/recordings`);
			const items = (await response.json()) as { status: string }[];
			return items[0]?.status ?? 'none';
		};

		await host.getByRole('button', { name: 'Start recording' }).click();
		await host.getByRole('button', { name: 'Record?' }).click();
		await expect.poll(status, { timeout: 90_000 }).toBe('recording');

		// The "crash": the host's browser context dies mid-recording.
		await hostContext.close();
		await guest.waitForTimeout(5_000);
		expect(await status()).toBe('recording');

		// The recording is still the server's to control.
		const stopped = await api.post(`/api/rooms/${slug}/recording/stop`, {
			headers: { 'X-Klisi-Csrf': '1' }
		});
		expect(stopped.ok()).toBe(true);
		await expect.poll(status, { timeout: 90_000 }).toBe('completed');
		await api.dispose();
	} finally {
		for (const context of contexts) await context.close().catch(() => {});
		await browser.close();
	}
});
