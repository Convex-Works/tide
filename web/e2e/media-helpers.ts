import { expect, type Page, type TestInfo } from '@playwright/test';

export interface ProbePublication {
  publicationSid: string;
  source: string;
  kind: string;
  muted: boolean;
  subscribed: boolean;
  desired: boolean;
  permissionAllowed: boolean;
  subscriptionStatus: string;
  streamState: string;
  track?: { id: string; readyState: MediaStreamTrackState };
  failure?: {
    recoverable: boolean;
    message: string;
  };
}

export interface MediaProbeSnapshot {
  connectionState: string;
  playback: { audio: boolean; video: boolean; error: string };
  /**
   * `ticks` counts reconciles (the heartbeat drives these even when idle);
   * `revisions` counts the ones that actually republished the projection.
   * A growing tick count with a flat revision count is the no-churn property.
   */
  projection: { ticks: number; revisions: number };
  participants: {
    identity: string;
    isLocal: boolean;
    isEgress: boolean;
    publications: Record<string, ProbePublication>;
  }[];
  elements: {
    publicationSid: string;
    source: string;
    kind: string;
    srcObjectTrackIds: string[];
    attachedInLiveKit: boolean;
  }[];
  inbound: Record<
    string,
    {
      bytesReceived: number;
      framesDecoded: number;
      packetsLost: number;
      jitter: number;
      audioLevel: number;
      totalAudioEnergy: number;
    }
  >;
  subscriptionFailures: Record<
    string,
    {
      recoverable: boolean;
      message: string;
    }
  >;
}

type SyntheticResource =
  MediaStreamTrack | AudioContext | OscillatorNode | HTMLCanvasElement | number;

type MediaTestWindow = Window &
  typeof globalThis & {
    __klisiRoom: {
      state: string;
      remoteParticipants: Map<string, { identity: string }>;
      localParticipant: {
        trackPublications: Map<
          string,
          {
            trackSid: string;
            source: string;
            track?: unknown;
            mute(): Promise<void>;
            unmute(): Promise<void>;
          }
        >;
        publishTrack(
          track: MediaStreamTrack,
          options: { source: string; name: string; simulcast?: boolean }
        ): Promise<{ trackSid: string }>;
        unpublishTrack(track: unknown, stopOnUnpublish?: boolean): Promise<unknown>;
      };
      emit(event: string, participants: unknown[]): void;
      simulateScenario(scenario: string, arg?: unknown): Promise<void>;
    };
    __klisiMediaTest: {
      attachmentDelayMs: number;
      snapshot(): Promise<MediaProbeSnapshot>;
      reconcile(): void;
      clearSrcObject(publicationSid: string): boolean;
      forgetLiveKitAttachment(publicationSid: string): boolean;
      injectSubscriptionFailure(publicationSid: string): void;
      unsubscribeBehindBack(publicationSid: string): boolean;
      rejectNextPlayback(kind?: 'audio' | 'video' | 'any'): void;
      blockPlayback(kind?: 'audio' | 'video' | 'any'): void;
      unblockPlayback(): void;
    };
    __klisiSynthetic?: {
      resources: SyntheticResource[];
      cleanup: (() => void)[];
    };
  };

export async function joinMediaTestRoom(
  page: Page,
  room: string,
  name: string,
  localMedia = false
): Promise<void> {
  await page.goto('/dev');
  await page.fill('input[name="room"]', room);
  await page.fill('input[name="name"]', name);

  if (!localMedia) {
    const cameraToggle = page.getByRole('button', { name: 'Turn camera off' });
    if (await cameraToggle.isVisible().catch(() => false)) await cameraToggle.click();
    const microphoneToggle = page.getByRole('button', { name: 'Mute microphone' }).first();
    if (await microphoneToggle.isVisible().catch(() => false)) await microphoneToggle.click();
  }

  await page.getByRole('button', { name: 'Join meeting' }).click();
  await expect(page.getByRole('button', { name: 'Share screen' })).toBeVisible({
    timeout: 30_000
  });
  await expect
    .poll(
      () =>
        page.evaluate(() => (window as MediaTestWindow).__klisiRoom?.state ?? 'test-hook-missing'),
      { timeout: 30_000 }
    )
    .toBe('connected');
}

export async function publishSyntheticCameraAndAudio(
  page: Page
): Promise<{ cameraSid: string; microphoneSid: string }> {
  return page.evaluate(async () => {
    const testWindow = window as MediaTestWindow;
    const room = testWindow.__klisiRoom;
    const resources: SyntheticResource[] = [];
    const cleanup: (() => void)[] = [];

    const canvas = document.createElement('canvas');
    canvas.width = 640;
    canvas.height = 360;
    canvas.hidden = true;
    document.body.append(canvas);
    resources.push(canvas);
    const context = canvas.getContext('2d');
    if (!context) throw new Error('Canvas 2D context is unavailable.');
    let frame = 0;
    let animationFrame = 0;
    const draw = () => {
      frame += 1;
      context.fillStyle = `hsl(${frame % 360} 75% 45%)`;
      context.fillRect(0, 0, canvas.width, canvas.height);
      context.fillStyle = 'white';
      context.font = 'bold 52px sans-serif';
      context.fillText(String(frame), 32, 72);
      animationFrame = requestAnimationFrame(draw);
    };
    draw();
    cleanup.push(() => cancelAnimationFrame(animationFrame));
    const videoTrack = canvas.captureStream(15).getVideoTracks()[0];
    resources.push(videoTrack);
    const camera = await room.localParticipant.publishTrack(videoTrack, {
      source: 'camera',
      name: 'synthetic-camera'
    });

    const audioContext = new AudioContext();
    await audioContext.resume();
    const oscillator = audioContext.createOscillator();
    const gain = audioContext.createGain();
    const destination = audioContext.createMediaStreamDestination();
    oscillator.frequency.value = 440;
    gain.gain.value = 0.16;
    oscillator.connect(gain).connect(destination);
    oscillator.start();
    resources.push(audioContext, oscillator);
    const audioTrack = destination.stream.getAudioTracks()[0];
    resources.push(audioTrack);
    const microphone = await room.localParticipant.publishTrack(audioTrack, {
      source: 'microphone',
      name: 'synthetic-microphone'
    });
    testWindow.__klisiSynthetic = { resources, cleanup };
    return { cameraSid: camera.trackSid, microphoneSid: microphone.trackSid };
  });
}

export async function publishSyntheticScreen(
  page: Page,
  withAudio = true
): Promise<{ screenSid: string; audioSid?: string }> {
  return page.evaluate(async (publishAudio) => {
    const testWindow = window as MediaTestWindow;
    const room = testWindow.__klisiRoom;
    const resources = testWindow.__klisiSynthetic?.resources ?? ([] as SyntheticResource[]);
    const cleanup = testWindow.__klisiSynthetic?.cleanup ?? [];
    const canvas = document.createElement('canvas');
    canvas.width = 800;
    canvas.height = 450;
    canvas.hidden = true;
    document.body.append(canvas);
    resources.push(canvas);
    const context = canvas.getContext('2d');
    if (!context) throw new Error('Canvas 2D context is unavailable.');
    let frame = 0;
    let animationFrame = 0;
    const draw = () => {
      frame += 1;
      context.fillStyle = frame % 2 ? '#1d4ed8' : '#7c3aed';
      context.fillRect(0, 0, canvas.width, canvas.height);
      context.fillStyle = 'white';
      context.font = 'bold 64px sans-serif';
      context.fillText(`screen ${frame}`, 40, 90);
      animationFrame = requestAnimationFrame(draw);
    };
    draw();
    cleanup.push(() => cancelAnimationFrame(animationFrame));
    const track = canvas.captureStream(12).getVideoTracks()[0];
    resources.push(track);
    const screen = await room.localParticipant.publishTrack(track, {
      source: 'screen_share',
      name: `synthetic-screen-${Date.now()}`,
      simulcast: false
    });

    let audioSid: string | undefined;
    if (publishAudio) {
      const audioContext = new AudioContext();
      await audioContext.resume();
      const oscillator = audioContext.createOscillator();
      const gain = audioContext.createGain();
      const destination = audioContext.createMediaStreamDestination();
      oscillator.frequency.value = 660;
      gain.gain.value = 0.12;
      oscillator.connect(gain).connect(destination);
      oscillator.start();
      resources.push(audioContext, oscillator);
      const audioTrack = destination.stream.getAudioTracks()[0];
      resources.push(audioTrack);
      const publication = await room.localParticipant.publishTrack(audioTrack, {
        source: 'screen_share_audio',
        name: `synthetic-screen-audio-${Date.now()}`
      });
      audioSid = publication.trackSid;
    }
    testWindow.__klisiSynthetic = { resources, cleanup };
    return { screenSid: screen.trackSid, audioSid };
  }, withAudio);
}

export async function unpublishSynthetic(page: Page, publicationSids: string[]): Promise<void> {
  await page.evaluate(async (sids) => {
    const room = (window as MediaTestWindow).__klisiRoom;
    for (const sid of sids) {
      const publication = room.localParticipant.trackPublications.get(sid);
      if (publication?.track) {
        await room.localParticipant.unpublishTrack(publication.track, true);
      }
    }
  }, publicationSids);
}

export async function publishReplacementCamera(page: Page): Promise<string> {
  return page.evaluate(async () => {
    const testWindow = window as MediaTestWindow;
    const resources = testWindow.__klisiSynthetic?.resources ?? ([] as SyntheticResource[]);
    const cleanup = testWindow.__klisiSynthetic?.cleanup ?? [];
    const canvas = document.createElement('canvas');
    canvas.width = 640;
    canvas.height = 360;
    canvas.hidden = true;
    document.body.append(canvas);
    resources.push(canvas);
    const context = canvas.getContext('2d');
    if (!context) throw new Error('Canvas 2D context is unavailable.');
    let frame = 0;
    let animationFrame = 0;
    const draw = () => {
      frame += 1;
      context.fillStyle = `hsl(${(frame * 3) % 360} 70% 38%)`;
      context.fillRect(0, 0, canvas.width, canvas.height);
      context.fillStyle = 'white';
      context.font = 'bold 44px sans-serif';
      context.fillText(`replacement ${frame}`, 24, 64);
      animationFrame = requestAnimationFrame(draw);
    };
    draw();
    cleanup.push(() => cancelAnimationFrame(animationFrame));
    const track = canvas.captureStream(15).getVideoTracks()[0];
    resources.push(track);
    const publication = await testWindow.__klisiRoom.localParticipant.publishTrack(track, {
      source: 'camera',
      name: `replacement-camera-${Date.now()}`
    });
    testWindow.__klisiSynthetic = { resources, cleanup };
    return publication.trackSid;
  });
}

export async function publishReplacementMicrophone(page: Page): Promise<string> {
  return page.evaluate(async () => {
    const testWindow = window as MediaTestWindow;
    const resources = testWindow.__klisiSynthetic?.resources ?? ([] as SyntheticResource[]);
    const cleanup = testWindow.__klisiSynthetic?.cleanup ?? [];
    const audioContext = new AudioContext();
    await audioContext.resume();
    const oscillator = audioContext.createOscillator();
    const gain = audioContext.createGain();
    const destination = audioContext.createMediaStreamDestination();
    oscillator.frequency.value = 520;
    gain.gain.value = 0.14;
    oscillator.connect(gain).connect(destination);
    oscillator.start();
    resources.push(audioContext, oscillator);
    const track = destination.stream.getAudioTracks()[0];
    resources.push(track);
    const publication = await testWindow.__klisiRoom.localParticipant.publishTrack(track, {
      source: 'microphone',
      name: `replacement-microphone-${Date.now()}`
    });
    testWindow.__klisiSynthetic = { resources, cleanup };
    return publication.trackSid;
  });
}

export async function resetSyntheticMedia(
  page: Page
): Promise<{ cameraSid: string; microphoneSid: string }> {
  await page.evaluate(async () => {
    const testWindow = window as MediaTestWindow;
    const publications = [...testWindow.__klisiRoom.localParticipant.trackPublications.values()];
    for (const publication of publications) {
      if (publication.track) {
        await testWindow.__klisiRoom.localParticipant.unpublishTrack(publication.track, true);
      }
    }

    for (const stop of testWindow.__klisiSynthetic?.cleanup ?? []) stop();
    for (const resource of testWindow.__klisiSynthetic?.resources ?? []) {
      if (resource instanceof MediaStreamTrack) resource.stop();
      else if (resource instanceof OscillatorNode) {
        try {
          resource.stop();
        } catch {
          // Already stopped.
        }
      } else if (resource instanceof AudioContext) {
        await resource.close().catch(() => undefined);
      } else if (resource instanceof HTMLCanvasElement) {
        resource.remove();
      }
    }
    testWindow.__klisiSynthetic = { resources: [], cleanup: [] };
  });
  return publishSyntheticCameraAndAudio(page);
}

export async function setPublicationMuted(
  page: Page,
  publicationSid: string,
  muted: boolean
): Promise<void> {
  await page.evaluate(
    async ({ sid, nextMuted }) => {
      const publication = (
        window as MediaTestWindow
      ).__klisiRoom.localParticipant.trackPublications.get(sid);
      if (!publication) throw new Error(`Publication ${sid} is missing.`);
      if (nextMuted) await publication.mute();
      else await publication.unmute();
    },
    { sid: publicationSid, nextMuted: muted }
  );
}

export async function probe(page: Page): Promise<MediaProbeSnapshot> {
  return page.evaluate(() => (window as MediaTestWindow).__klisiMediaTest.snapshot());
}

/**
 * A publication that has just been re-subscribed has to renegotiate before RTP
 * flows again, and the SDK reports `subscribed` well before the first frames
 * arrive. RTP assertions need this even when the DOM has already converged. It
 * still fails if media never returns — it only stops the assertion racing the
 * renegotiation or the browser's stats cadence.
 */
export const resubscribeSettleTimeout = 20_000;

const mediaPollIntervals = [200, 300, 500];

function expectedMediaElements(snapshot: MediaProbeSnapshot): ProbePublication[] {
  // A publishing client renders its own camera (and screen share) in its own
  // tile, so those elements are expected too. Local audio is never rendered —
  // RemoteAudioRenderer skips the local participant.
  return snapshot.participants.flatMap((participant) =>
    Object.values(participant.publications).filter((publication) =>
      participant.isLocal
        ? publication.kind === 'video' && !publication.muted && publication.track !== undefined
        : publication.subscribed &&
          publication.permissionAllowed &&
          publication.track !== undefined &&
          (publication.kind === 'audio' || !publication.muted)
    )
  );
}

function expectedFlowingPublications(snapshot: MediaProbeSnapshot): ProbePublication[] {
  return snapshot.participants
    .filter((participant) => !participant.isLocal)
    .flatMap((participant) => Object.values(participant.publications))
    .filter(
      (publication) =>
        publication.desired &&
        publication.subscribed &&
        !publication.muted &&
        publication.permissionAllowed &&
        publication.track
    );
}

function remotePublications(snapshot: MediaProbeSnapshot): ProbePublication[] {
  return snapshot.participants
    .filter((participant) => !participant.isLocal)
    .flatMap((participant) => Object.values(participant.publications));
}

export interface MediaPublicationRequirements {
  /** Remote publications that must be subscribed and match their expected mute state. */
  requiredSids?: readonly string[];
  /** Required publications expected to be muted; all other required SIDs must be unmuted. */
  requiredMutedSids?: readonly string[];
  /** Remote publications that must be unsubscribed and absent from the DOM. */
  forbiddenSids?: readonly string[];
  /** Participants that must be present, including the local participant when required. */
  requiredParticipantIdentities?: readonly string[];
  /** Participants that must be absent after their departure has been signalled. */
  forbiddenParticipantIdentities?: readonly string[];
}

function participantRequirementDiagnostics(
  snapshot: MediaProbeSnapshot,
  requirements: MediaPublicationRequirements
) {
  const participants = snapshot.participants
    .map(({ identity, isLocal, isEgress }) => ({ identity, isLocal, isEgress }))
    .sort((left, right) => left.identity.localeCompare(right.identity));
  const requiredIdentities = [...new Set(requirements.requiredParticipantIdentities ?? [])].sort();
  const forbiddenIdentities = [
    ...new Set(requirements.forbiddenParticipantIdentities ?? [])
  ].sort();
  const required = requiredIdentities.map((identity) => {
    const participant = participants.find((candidate) => candidate.identity === identity);
    return { identity, ok: participant !== undefined, participant: participant ?? null };
  });
  const forbidden = forbiddenIdentities.map((identity) => {
    const participant = participants.find((candidate) => candidate.identity === identity);
    return { identity, ok: participant === undefined, participant: participant ?? null };
  });
  return {
    // This is deliberately not an exact-set comparison. Recording egress and
    // other unmodelled participants may coexist with the identities pinned by
    // a caller; only an explicitly forbidden identity is rejected.
    ok:
      required.every((participant) => participant.ok) &&
      forbidden.every((participant) => participant.ok),
    actual: participants,
    required,
    forbidden
  };
}

function publicationRequirementDiagnostics(
  snapshot: MediaProbeSnapshot,
  requirements: MediaPublicationRequirements
) {
  const publications = remotePublications(snapshot);
  const requiredSids = [...new Set(requirements.requiredSids ?? [])].sort();
  const requiredMutedSids = new Set(requirements.requiredMutedSids ?? []);
  const forbiddenSids = [...new Set(requirements.forbiddenSids ?? [])].sort();
  const required = requiredSids.map((publicationSid) => {
    const publication = publications.find(
      (candidate) => candidate.publicationSid === publicationSid
    );
    const element = snapshot.elements.find(
      (candidate) => candidate.publicationSid === publicationSid
    );
    const expectedMuted = requiredMutedSids.has(publicationSid);
    // Remote audio keeps its renderer while muted. Muted video does not, so
    // its pinned state is a subscribed publication with no media element.
    const elementExpected =
      publication === undefined ? null : publication.kind === 'audio' || !expectedMuted;
    const attached = element?.attachedInLiveKit === true && element.srcObjectTrackIds.length > 0;
    const elementOk = elementExpected === true ? attached : elementExpected === false && !element;
    return {
      publicationSid,
      ok:
        publication !== undefined &&
        publication.subscribed &&
        publication.permissionAllowed &&
        publication.track !== undefined &&
        publication.muted === expectedMuted &&
        elementOk,
      expectedMuted,
      elementExpected,
      sdk: publication
        ? {
            desired: publication.desired,
            subscribed: publication.subscribed,
            muted: publication.muted,
            permissionAllowed: publication.permissionAllowed,
            subscriptionStatus: publication.subscriptionStatus,
            streamState: publication.streamState,
            track: publication.track
          }
        : null,
      dom: element ?? null,
      recoverableError: snapshot.subscriptionFailures[publicationSid] ?? null
    };
  });
  const forbidden = forbiddenSids.map((publicationSid) => {
    const publication = publications.find(
      (candidate) => candidate.publicationSid === publicationSid
    );
    const element = snapshot.elements.find(
      (candidate) => candidate.publicationSid === publicationSid
    );
    return {
      publicationSid,
      ok: publication?.subscribed !== true && element === undefined,
      subscribed: publication?.subscribed ?? false,
      sdk: publication ?? null,
      dom: element ?? null
    };
  });
  return {
    ok:
      required.every((publication) => publication.ok) &&
      forbidden.every((publication) => publication.ok),
    required,
    forbidden
  };
}

function publicationElementSet(
  snapshot: MediaProbeSnapshot,
  expected: ProbePublication[]
): {
  exact: boolean;
  diagnostics: {
    expected: string[];
    actual: string[];
    missing: string[];
    unexpected: string[];
  };
} {
  const expectedSids = expected.map((publication) => publication.publicationSid).sort();
  const actualSids = snapshot.elements.map((element) => element.publicationSid).sort();
  return {
    exact:
      expectedSids.length === actualSids.length &&
      expectedSids.every((sid, index) => sid === actualSids[index]),
    diagnostics: {
      expected: expectedSids,
      actual: actualSids,
      missing: expectedSids.filter((sid) => !actualSids.includes(sid)),
      unexpected: actualSids.filter((sid) => !expectedSids.includes(sid))
    }
  };
}

export interface MediaStructureOptions extends MediaPublicationRequirements {
  timeout?: number;
}

function mediaPollError(phase: 'structure' | 'flow', cause: unknown, diagnostics: unknown): Error {
  const assertion = cause instanceof Error ? cause.message : String(cause);
  return new Error(
    `Media ${phase} invariant did not converge.\nLast diagnostics:\n${JSON.stringify(diagnostics, null, 2)}\n\nPlaywright assertion:\n${assertion}`,
    { cause }
  );
}

export async function expectMediaStructure(
  page: Page,
  options: MediaStructureOptions = {}
): Promise<MediaProbeSnapshot> {
  const timeout = options.timeout ?? 3_000;
  let latest = await probe(page);
  let latestDiagnostics: unknown = { ok: false, status: 'not-polled' };
  try {
    await expect
      .poll(
        async () => {
          latest = await probe(page);
          const expected = expectedMediaElements(latest);
          const elementSet = publicationElementSet(latest, expected);
          const requirements = publicationRequirementDiagnostics(latest, options);
          const participantRequirements = participantRequirementDiagnostics(latest, options);
          const publicationDiagnostics = expected.map((publication) => {
            const element = latest.elements.find(
              (candidate) => candidate.publicationSid === publication.publicationSid
            );
            const attached =
              element?.attachedInLiveKit === true && element.srcObjectTrackIds.length > 0;
            return {
              publicationSid: publication.publicationSid,
              source: publication.source,
              kind: publication.kind,
              ok: attached,
              sdk: {
                desired: publication.desired,
                subscribed: publication.subscribed,
                muted: publication.muted,
                permissionAllowed: publication.permissionAllowed,
                subscriptionStatus: publication.subscriptionStatus,
                streamState: publication.streamState,
                track: publication.track
              },
              dom: element ?? null,
              recoverableError: latest.subscriptionFailures[publication.publicationSid] ?? null
            };
          });
          latestDiagnostics = {
            ok:
              elementSet.exact &&
              publicationDiagnostics.every((publication) => publication.ok) &&
              requirements.ok &&
              participantRequirements.ok,
            publicationElementSet: elementSet.diagnostics,
            requirements,
            participantRequirements,
            publications: publicationDiagnostics
          };
          return latestDiagnostics;
        },
        { timeout, intervals: mediaPollIntervals }
      )
      .toMatchObject({ ok: true });
  } catch (cause) {
    throw mediaPollError('structure', cause, latestDiagnostics);
  }
  return latest;
}

export interface MediaInvariantOptions extends MediaPublicationRequirements {
  structureTimeout?: number;
  flowTimeout?: number;
}

export async function expectMediaInvariant(
  page: Page,
  options: MediaInvariantOptions = {}
): Promise<MediaProbeSnapshot> {
  const baseline = await probe(page);
  const structureTimeout = options.structureTimeout ?? 3_000;
  const flowTimeout = options.flowTimeout ?? resubscribeSettleTimeout;
  await expectMediaStructure(page, {
    timeout: structureTimeout,
    requiredSids: options.requiredSids,
    requiredMutedSids: options.requiredMutedSids,
    forbiddenSids: options.forbiddenSids,
    requiredParticipantIdentities: options.requiredParticipantIdentities,
    forbiddenParticipantIdentities: options.forbiddenParticipantIdentities
  });
  let latest = baseline;
  let latestDiagnostics: unknown = { ok: false, status: 'not-polled' };
  try {
    await expect
      .poll(
        async () => {
          latest = await probe(page);
          const requiredSids = new Set(options.requiredSids ?? []);
          const requiredMutedSids = new Set(options.requiredMutedSids ?? []);
          const flowingSids = new Set(
            expectedFlowingPublications(latest).map((publication) => publication.publicationSid)
          );
          const desired = remotePublications(latest).filter(
            (publication) =>
              flowingSids.has(publication.publicationSid) ||
              (requiredSids.has(publication.publicationSid) &&
                !requiredMutedSids.has(publication.publicationSid))
          );
          const elementSet = publicationElementSet(latest, expectedMediaElements(latest));
          const requirements = publicationRequirementDiagnostics(latest, options);
          const publicationDiagnostics = desired.map((publication) => {
            const element = latest.elements.find(
              (candidate) => candidate.publicationSid === publication.publicationSid
            );
            const failure = latest.subscriptionFailures[publication.publicationSid];
            const before = baseline.inbound[publication.publicationSid] ?? {
              bytesReceived: 0,
              framesDecoded: 0,
              packetsLost: 0,
              jitter: 0,
              audioLevel: 0,
              totalAudioEnergy: 0
            };
            const after = latest.inbound[publication.publicationSid];
            const advancing =
              after !== undefined &&
              (publication.kind === 'video'
                ? after.bytesReceived > before.bytesReceived ||
                  after.framesDecoded > before.framesDecoded
                : after.bytesReceived > before.bytesReceived ||
                  after.totalAudioEnergy > before.totalAudioEnergy ||
                  after.audioLevel > 0);
            const attached =
              element?.attachedInLiveKit === true && element.srcObjectTrackIds.length > 0;
            return {
              publicationSid: publication.publicationSid,
              source: publication.source,
              kind: publication.kind,
              // A recorded failure no longer excuses a publication. Recovery is
              // the reconcile tick re-deriving the subscription, so "recoverable"
              // means it must actually recover inside this flow poll window.
              ok: advancing,
              attached,
              sdk: {
                desired: publication.desired,
                subscribed: publication.subscribed,
                muted: publication.muted,
                permissionAllowed: publication.permissionAllowed,
                subscriptionStatus: publication.subscriptionStatus,
                streamState: publication.streamState,
                track: publication.track
              },
              dom: element ?? null,
              recoverableError: failure ?? null,
              stats: {
                before,
                after: after ?? null,
                delta: after
                  ? {
                      bytesReceived: after.bytesReceived - before.bytesReceived,
                      framesDecoded: after.framesDecoded - before.framesDecoded,
                      packetsLost: after.packetsLost - before.packetsLost,
                      jitter: after.jitter - before.jitter,
                      audioEnergy: after.totalAudioEnergy - before.totalAudioEnergy
                    }
                  : null
              }
            };
          });
          latestDiagnostics = {
            ok: requirements.ok && publicationDiagnostics.every((publication) => publication.ok),
            publicationElementSet: elementSet.diagnostics,
            requirements,
            publications: publicationDiagnostics
          };
          return latestDiagnostics;
        },
        { timeout: flowTimeout, intervals: mediaPollIntervals }
      )
      .toMatchObject({ ok: true });
  } catch (cause) {
    throw mediaPollError('flow', cause, latestDiagnostics);
  }
  return latest;
}

export async function tagMediaElements(page: Page): Promise<Record<string, string>> {
  return page.evaluate(() => {
    const tagged: Record<string, string> = {};
    for (const element of document.querySelectorAll<HTMLMediaElement>('[data-publication-sid]')) {
      const sid = element.dataset.publicationSid;
      if (!sid) continue;
      const token =
        element.dataset.instanceToken ??
        `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
      element.dataset.instanceToken = token;
      tagged[sid] = token;
    }
    return tagged;
  });
}

export async function expectMediaElementTags(
  page: Page,
  expected: Record<string, string>
): Promise<void> {
  await expect.poll(() => tagMediaElements(page), { timeout: 3_000 }).toMatchObject(expected);
}

// A page that spins can emit millions of console lines, and an unbounded
// capture then fails the *attachment* instead of reporting the spin. Keep the
// tail and say how much was dropped.
const maximumCapturedMessages = 4_000;

export function captureBrowserDiagnostics(page: Page): string[] {
  const messages: string[] = [];
  let dropped = 0;
  const record = (line: string): void => {
    messages.push(line);
    if (messages.length > maximumCapturedMessages) {
      messages.splice(0, messages.length - maximumCapturedMessages);
      dropped += 1;
      if (dropped % 1_000 === 1) messages[0] = `[dropped ${dropped} earlier messages]`;
    }
  };
  page.on('console', (message) => record(`[console:${message.type()}] ${message.text()}`));
  page.on('pageerror', (error) => record(`[pageerror] ${error.stack ?? error.message}`));
  return messages;
}

export async function attachDiagnostics(
  testInfo: TestInfo,
  name: string,
  diagnostics: unknown
): Promise<void> {
  await testInfo.attach(name, {
    body: Buffer.from(JSON.stringify(diagnostics, null, 2)),
    contentType: 'application/json'
  });
}
