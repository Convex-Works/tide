import { test, expect, chromium, type BrowserContext } from '@playwright/test';
import { createRoomAndGetSlug, dexLogin, joinAsGuestThroughLobby } from './helpers';

test('participants hide remote video locally while host moderation stays gated', async () => {
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
    // Chrome's fake microphone can hang on macOS. Supply a deterministic
    // Web Audio track while preserving Chromium's fake camera.
    await context.addInitScript(() => {
      const nativeGetUserMedia = MediaDevices.prototype.getUserMedia;
      const audioResources: Array<{ context: AudioContext; oscillator: OscillatorNode }> = [];
      MediaDevices.prototype.getUserMedia = async function (
        constraints?: MediaStreamConstraints
      ): Promise<MediaStream> {
        if (!constraints?.audio) return nativeGetUserMedia.call(this, constraints);

        const stream = constraints.video
          ? await nativeGetUserMedia.call(this, { ...constraints, audio: false })
          : new MediaStream();
        const audioContext = new AudioContext();
        const oscillator = audioContext.createOscillator();
        const destination = audioContext.createMediaStreamDestination();
        oscillator.connect(destination);
        oscillator.start();
        stream.addTrack(destination.stream.getAudioTracks()[0]);
        audioResources.push({ context: audioContext, oscillator });
        return stream;
      };
    });
    contexts.push(context);
    return context;
  };

  try {
    const host = await (await newContext()).newPage();
    await dexLogin(host);
    const slug = await createRoomAndGetSlug(host, `Moderation e2e ${Date.now()}`);
    await host.goto(`/m/${slug}`);
    await host.getByRole('button', { name: 'Join room' }).click();
    await expect(host.getByRole('button', { name: 'Share screen' })).toBeVisible({
      timeout: 20_000
    });

    const guest = await (await newContext()).newPage();
    await joinAsGuestThroughLobby(guest, host, slug, 'Moderated guest');
    await host.getByRole('button', { name: 'Close people' }).click();

    // Every viewer can opt out of a remote camera. This is a real LiveKit
    // unsubscribe, not merely a hidden DOM element, so it saves bandwidth.
    const hostTileForGuest = host
      .getByTestId('participant-tile')
      .filter({ hasText: 'Moderated guest' });
    await expect(hostTileForGuest).toBeVisible({ timeout: 20_000 });
    const guestIdentity = await hostTileForGuest.getAttribute('data-identity');
    expect(guestIdentity).toBeTruthy();

    await hostTileForGuest.getByRole('button', { name: 'Actions for Moderated guest' }).click();
    const hostMenu = host.getByRole('menu', { name: 'Moderated guest actions' });
    await expect(hostMenu.getByRole('menuitem', { name: 'Hide video' })).toBeVisible();
    await expect(hostMenu.getByRole('menuitem', { name: 'Mute' })).toBeVisible();
    await expect(hostMenu.getByRole('menuitem', { name: 'Remove' })).toBeVisible();
    await hostMenu.getByRole('menuitem', { name: 'Hide video' }).click();

    await expect(hostTileForGuest).toHaveAttribute('data-video-hidden', 'true');
    await expect(hostTileForGuest.getByText('Video hidden')).toBeVisible();
    await expect
      .poll(
        () =>
          host.evaluate((identity) => {
            const room = (
              window as Window & {
                __klisiRoom?: {
                  remoteParticipants: Map<
                    string,
                    {
                      videoTrackPublications: Map<string, { source: string; isDesired: boolean }>;
                    }
                  >;
                };
              }
            ).__klisiRoom;
            const participant = room?.remoteParticipants.get(identity ?? '');
            return [...(participant?.videoTrackPublications.values() ?? [])].find(
              (publication) => publication.source === 'camera'
            )?.isDesired;
          }, guestIdentity),
        { timeout: 10_000 }
      )
      .toBe(false);

    await hostTileForGuest.getByRole('button', { name: 'Actions for Moderated guest' }).click();
    await hostMenu.getByRole('menuitem', { name: 'Show video' }).click();
    await expect(hostTileForGuest).toHaveAttribute('data-video-hidden', 'false');
    await expect
      .poll(
        () =>
          host.evaluate((identity) => {
            const room = (
              window as Window & {
                __klisiRoom?: {
                  remoteParticipants: Map<
                    string,
                    { videoTrackPublications: Map<string, { source: string; isDesired: boolean }> }
                  >;
                };
              }
            ).__klisiRoom;
            const participant = room?.remoteParticipants.get(identity ?? '');
            return [...(participant?.videoTrackPublications.values() ?? [])].find(
              (publication) => publication.source === 'camera'
            )?.isDesired;
          }, guestIdentity),
        { timeout: 10_000 }
      )
      .toBe(true);

    // A guest gets the same local hide/show action, but never host moderation.
    const guestTileForHost = guest.locator('[data-testid="participant-tile"]:not(:has(.you))');
    await expect(guestTileForHost).toBeVisible({ timeout: 20_000 });
    await guestTileForHost.getByRole('button', { name: /^Actions for / }).click();
    const guestMenu = guest.getByRole('menu');
    await expect(guestMenu.getByRole('menuitem', { name: 'Hide video' })).toBeVisible();
    await expect(guestMenu.getByRole('menuitem', { name: 'Mute' })).toHaveCount(0);
    await expect(guestMenu.getByRole('menuitem', { name: 'Remove' })).toHaveCount(0);
    await guestMenu.getByRole('menuitem', { name: 'Hide video' }).click();
    await expect(guestTileForHost).toHaveAttribute('data-video-hidden', 'true');
    await guestTileForHost.getByRole('button', { name: /^Actions for / }).click();
    await guestMenu.getByRole('menuitem', { name: 'Show video' }).click();

    // Host-only actions live in the same participant menu.
    await hostTileForGuest.getByRole('button', { name: 'Actions for Moderated guest' }).click();
    await hostMenu.getByRole('menuitem', { name: 'Mute' }).click();

    await expect(hostTileForGuest.getByLabel('Microphone muted')).toBeVisible({ timeout: 20_000 });
    await expect(guest.getByRole('button', { name: 'Unmute microphone' })).toBeVisible({
      timeout: 20_000
    });

    await hostTileForGuest.getByRole('button', { name: 'Actions for Moderated guest' }).click();
    await hostMenu.getByRole('menuitem', { name: 'Remove', exact: true }).click();
    await hostMenu.getByRole('menuitem', { name: 'Remove?' }).click();
    await expect(guest.getByText('You were removed from the meeting.')).toBeVisible({
      timeout: 20_000
    });
    await expect(hostTileForGuest).toHaveCount(0, { timeout: 20_000 });

    const openPeople = host.getByRole('button', { name: 'Open people' });
    if (await openPeople.isVisible()) await openPeople.click();
    const people = host.getByRole('complementary', { name: 'People' });
    await guest.getByRole('button', { name: 'Request to rejoin' }).click();
    await guest.getByRole('button', { name: 'Join room' }).click();
    await expect(guest.getByText('Waiting for the host to let you in.')).toBeVisible({
      timeout: 20_000
    });
    await expect(people.getByText('Moderated guest', { exact: true })).toBeVisible({
      timeout: 20_000
    });
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
    await browser.close();
  }
});
