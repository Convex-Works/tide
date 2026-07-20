import { test, expect, chromium, type BrowserContext } from '@playwright/test';
import {
  capturedCueDetunes,
  createRoomAndGetSlug,
  dexLogin,
  installCueCapture,
  resetCueCapture
} from './helpers';

test('lobby and participant changes play their cues while the host moderates guests', async () => {
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

  try {
    const hostContext = await newContext();
    const host = await hostContext.newPage();
    await installCueCapture(host);
    await dexLogin(host);
    const roomName = `Lobby e2e ${Date.now()}`;
    const slug = await createRoomAndGetSlug(host, roomName);
    await host.goto(`/m/${slug}`);
    await host.getByRole('button', { name: 'Join room' }).click();
    await expect(host.getByRole('button', { name: 'Share screen' })).toBeVisible({
      timeout: 20_000
    });
    await expect.poll(() => capturedCueDetunes(host)).toEqual([-1200, -1200]);
    await resetCueCapture(host);

    const visitorContext = await newContext();
    const visitor = await visitorContext.newPage();
    await visitor.goto(`/m/${slug}`);
    await visitor.fill('input[name="name"]', 'Visitor');
    await visitor.getByRole('button', { name: 'Join room' }).click();
    await expect(visitor.getByText('Waiting for the host to let you in.')).toBeVisible({
      timeout: 20_000
    });
    const people = host.getByRole('complementary', { name: 'People' });
    await expect(people.getByText('Visitor', { exact: true })).toBeVisible({ timeout: 20_000 });
    await expect.poll(() => capturedCueDetunes(host)).toEqual([-500, -500, -1200, -1200]);

    await resetCueCapture(host);
    await people
      .getByRole('region', { name: 'Lobby' })
      .getByRole('button', { name: 'Admit' })
      .click();
    await expect(visitor.getByRole('button', { name: 'Share screen' })).toBeVisible({
      timeout: 20_000
    });
    await expect(host.getByTestId('participant-tile').filter({ hasText: 'Visitor' })).toBeVisible({
      timeout: 20_000
    });
    await expect.poll(() => capturedCueDetunes(host)).toEqual([-1200, -1200]);

    await resetCueCapture(host);
    await visitor.getByRole('button', { name: 'Leave room' }).click();
    await expect(host.getByTestId('participant-tile').filter({ hasText: 'Visitor' })).toHaveCount(
      0,
      {
        timeout: 20_000
      }
    );
    await expect.poll(() => capturedCueDetunes(host)).toEqual([0, 0]);

    const deniedContext = await newContext();
    const denied = await deniedContext.newPage();
    await denied.goto(`/m/${slug}`);
    await denied.fill('input[name="name"]', 'Second visitor');
    await denied.getByRole('button', { name: 'Join room' }).click();
    await expect(denied.getByText('Waiting for the host to let you in.')).toBeVisible({
      timeout: 20_000
    });
    await expect(people.getByText('Second visitor')).toBeVisible({ timeout: 20_000 });
    await people.getByRole('button', { name: 'Deny' }).click();
    await expect(denied.getByText('The host did not let you in.')).toBeVisible({
      timeout: 20_000
    });
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
    await browser.close();
  }
});
