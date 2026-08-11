import { test, expect } from '@playwright/test';
import { expectHealthy, expectMeshHealthy, expectObservedJoin } from './harness/assertions';
import { Meeting } from './harness/meeting';

/**
 * Recording adds a headless-Chrome egress participant to the room. It is a
 * subscriber like any other, so it is subject to the same late-joiner failure
 * — and from the meeting's side it is an extra participant arriving and
 * leaving mid-call, which must not disturb anybody's media.
 *
 * These run against real egress writing to real minio, so they are slower than
 * the rest of the gate; they stay in their own file so a failure here is
 * immediately distinguishable from a plain media failure.
 */

/** Statuses come from the egress webhook: starting → recording → completed. */
async function expectRecordingStatus(
  meeting: Meeting,
  allowed: string[],
  timeout = 120_000
): Promise<void> {
  await expect
    .poll(async () => (await meeting.recordings()).at(0)?.status ?? 'none', {
      timeout,
      intervals: [1_000, 2_000, 3_000]
    })
    .toMatch(new RegExp(`^(${allowed.join('|')})$`));
}

test('audio recording runs across a late join without disturbing the mesh', async ({ browser }) => {
  test.slow();
  const meeting = await Meeting.open(browser);
  try {
    const host = await meeting.join({ name: 'record-host', as: 'owner' });
    const guest = await meeting.join({ name: 'record-guest' });
    await expectMeshHealthy([host, guest]);

    await meeting.startRecording(false);
    // Wait for the egress webhook to report it actually running: stopping
    // while it is still 'starting' aborts the pipeline (LiveKit 412).
    await expectRecordingStatus(meeting, ['recording']);

    // Egress joins as a participant. The mesh must be unchanged: the probe
    // separates egress out, so any drift here is a real regression.
    await expectMeshHealthy([host, guest]);
    await expect(host.page.getByTestId('recording-chip')).toBeVisible({ timeout: 30_000 });
    await expect(guest.page.getByTestId('recording-chip')).toBeVisible({ timeout: 30_000 });

    // The incident scenario, but now with a recording in flight.
    await host.clearLedger();
    const latecomer = await meeting.join({ name: 'record-latecomer' });
    await expectMeshHealthy([host, guest, latecomer]);
    await expectObservedJoin(host, latecomer, ['camera', 'microphone']);

    await meeting.stopRecording();
    await expectRecordingStatus(meeting, ['completed']);

    const recordings = await meeting.recordings();
    expect(recordings).toHaveLength(1);
    expect(recordings[0].audio_only).toBe(true);

    // Stopping must leave the meeting itself untouched.
    await expectMeshHealthy([host, guest, latecomer]);
  } finally {
    await meeting.stopRecording().catch(() => undefined);
    await meeting.close();
  }
});

test('video recording composites a screen share and a late joiner', async ({ browser }) => {
  test.slow();
  const meeting = await Meeting.open(browser);
  try {
    const host = await meeting.join({ name: 'video-record-host', as: 'owner' });
    await meeting.startRecording(true);
    // Wait for the egress webhook to report it actually running: stopping
    // while it is still 'starting' aborts the pipeline (LiveKit 412).
    await expectRecordingStatus(meeting, ['recording']);

    const presenter = await meeting.join({ name: 'video-record-presenter' });
    await presenter.startScreenShare(true);
    await expectMeshHealthy([host, presenter]);
    await expectHealthy(host, [presenter]);

    await meeting.stopRecording();
    await expectRecordingStatus(meeting, ['completed']);

    const recordings = await meeting.recordings();
    expect(recordings).toHaveLength(1);
    expect(recordings[0].audio_only).toBe(false);
  } finally {
    await meeting.stopRecording().catch(() => undefined);
    await meeting.close();
  }
});
