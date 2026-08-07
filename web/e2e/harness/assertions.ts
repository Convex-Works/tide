import { expect } from '@playwright/test';
import { expectMediaInvariant } from '../media-helpers';
import type { LedgerEntry, MeetingActor } from './meeting';

/**
 * expectMediaInvariant proves that whatever a client *knows about* is attached
 * and flowing. It says nothing about whether the client was told at all, so it
 * passes vacuously against the failure this suite exists to catch: an existing
 * participant that never learns a late joiner published anything. These
 * assertions add the missing half — presence — and then chain the invariant.
 */

const pollIntervals = [200, 300, 500, 1_000];

interface ObservedPeer {
  identity: string;
  sources: string[];
}

function expectedPeers(peers: MeetingActor[]): ObservedPeer[] {
  return peers
    .map((peer) => ({ identity: peer.identity, sources: [...peer.sources].sort() }))
    .sort((a, b) => a.identity.localeCompare(b.identity));
}

async function observedPeers(actor: MeetingActor): Promise<ObservedPeer[]> {
  const snapshot = await actor.probe();
  return snapshot.participants
    .filter((participant) => !participant.isLocal && !participant.isEgress)
    .map((participant) => ({
      identity: participant.identity,
      sources: Object.values(participant.publications)
        .filter((publication) => publication.subscribed && publication.track !== undefined)
        .map((publication) => publication.source)
        .sort()
    }))
    .sort((a, b) => a.identity.localeCompare(b.identity));
}

/** Every listed peer is known, subscribed, and offering exactly these sources. */
export async function expectSees(
  actor: MeetingActor,
  peers: MeetingActor[],
  timeout = 25_000
): Promise<void> {
  await expect
    .poll(() => observedPeers(actor), { timeout, intervals: pollIntervals })
    .toEqual(expectedPeers(peers));
}

/**
 * The probe re-derives from LiveKit's own maps, so it reports what the SDK
 * knows even when klisi's projection is stale. The rendered tiles are the only
 * assertion that klisi actually propagated that state to the user.
 */
export async function expectRenderedTiles(
  actor: MeetingActor,
  peers: MeetingActor[],
  timeout = 25_000
): Promise<void> {
  const expected = [actor.identity, ...peers.map((peer) => peer.identity)].sort();
  await expect
    .poll(
      () =>
        actor.page
          .locator('[data-testid="participant-tile"]')
          .evaluateAll((tiles) =>
            tiles.map((tile) => (tile as HTMLElement).dataset.identity ?? '').sort()
          ),
      { timeout, intervals: pollIntervals }
    )
    .toEqual(expected);
}

/**
 * No listener may throw. LiveKit installs a participant's track-event
 * forwarding after it emits ParticipantConnected and its emitter does not
 * catch, so one escaped exception wires that participant to nothing for the
 * rest of the session — recoverable only by reload.
 */
export async function expectNoUncaughtErrors(actor: MeetingActor): Promise<void> {
  expect(
    await actor.uncaughtErrors(),
    `${actor.name} saw an uncaught error; a throwing handler severs LiveKit's event wiring`
  ).toEqual([]);
}

/**
 * The user-visible outcome only: this peer is known, rendered, attached and
 * receiving. Deliberately says nothing about handler hygiene, so a fault
 * scenario can assert the symptom separately from the cause and a failure
 * names which of the two actually broke.
 */
export async function expectConverged(
  actor: MeetingActor,
  peers: MeetingActor[],
  timeout = 25_000
): Promise<void> {
  await expectSees(actor, peers, timeout);
  await expectRenderedTiles(actor, peers, timeout);
  try {
    await expectMediaInvariant(actor.page, 10_000);
  } catch (cause) {
    // The invariant only reports ok:false, which cannot be acted on. Attach the
    // per-stage view — signalling, subscription, track, element, RTP — so a
    // failure says which stage broke rather than that something did.
    throw new Error(`${actor.name}: ${await stageReport(actor)}\n\n${(cause as Error).message}`);
  }
}

/** Converged, and no listener threw getting there. */
export async function expectHealthy(
  actor: MeetingActor,
  peers: MeetingActor[],
  timeout = 25_000
): Promise<void> {
  await expectConverged(actor, peers, timeout);
  await expectNoUncaughtErrors(actor);
}

/** Publication-by-publication view of where the media chain stopped. */
export async function stageReport(actor: MeetingActor): Promise<string> {
  const snapshot = await actor.probe();
  const rows = snapshot.participants.flatMap((participant) =>
    Object.values(participant.publications).map((publication) => {
      const element = snapshot.elements.find(
        (candidate) => candidate.publicationSid === publication.publicationSid
      );
      return {
        peer: participant.isLocal ? `${participant.identity} (local)` : participant.identity,
        source: publication.source,
        kind: publication.kind,
        subscribed: publication.subscribed,
        desired: publication.desired,
        muted: publication.muted,
        status: publication.subscriptionStatus,
        stream: publication.streamState,
        track: publication.track !== undefined,
        element: element
          ? { attached: element.attachedInLiveKit, srcObject: element.srcObjectTrackIds.length > 0 }
          : null,
        inbound: snapshot.inbound[publication.publicationSid] ?? null,
        failure: snapshot.subscriptionFailures[publication.publicationSid] ?? null
      };
    })
  );
  return JSON.stringify(
    { connectionState: snapshot.connectionState, playback: snapshot.playback, publications: rows },
    null,
    2
  );
}

/** The full mesh: every actor sees and hears every other actor. */
export async function expectMeshHealthy(actors: MeetingActor[], timeout = 25_000): Promise<void> {
  for (const actor of actors) {
    const peers = actors.filter((candidate) => candidate !== actor);
    await expectHealthy(actor, peers, timeout);
  }
}

function indexOfEvent(
  ledger: LedgerEntry[],
  event: string,
  match: (entry: LedgerEntry) => boolean
): number {
  return ledger.findIndex((entry) => entry.event === event && match(entry));
}

/** The first step of the acceptance sequence that has not happened yet. */
function joinSequenceGap(
  ledger: LedgerEntry[],
  peer: MeetingActor,
  sources: string[]
): string | undefined {
  const connectedAt = indexOfEvent(
    ledger,
    'ParticipantConnected',
    (entry) => entry.identity === peer.identity
  );
  if (connectedAt < 0) return `ParticipantConnected for ${peer.name}`;
  for (const source of sources) {
    for (const event of ['TrackPublished', 'TrackSubscribed']) {
      const at = indexOfEvent(
        ledger,
        event,
        (entry) => entry.identity === peer.identity && entry.detail === source
      );
      if (at <= connectedAt) return `${event}(${source}) for ${peer.name} after it connected`;
    }
  }
  return undefined;
}

/**
 * The acceptance sequence for a late joiner, read from the receiver's own
 * ledger: it was told the participant arrived, told about each publication,
 * and then subscribed to it — in that order.
 *
 * Polled, because subscription lands some way after the joiner finishes
 * publishing and callers deliberately assert this *before* convergence so a
 * failure names which half broke.
 */
export async function expectObservedJoin(
  actor: MeetingActor,
  peer: MeetingActor,
  sources: string[] = [...peer.sources],
  timeout = 25_000
): Promise<void> {
  const deadline = Date.now() + timeout;
  let ledger = await actor.ledger();
  let gap = joinSequenceGap(ledger, peer, sources);
  while (gap !== undefined && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 250));
    ledger = await actor.ledger();
    gap = joinSequenceGap(ledger, peer, sources);
  }

  // The ledger is registered ahead of klisi's handlers, so what it did record
  // for this peer distinguishes "LiveKit stopped forwarding" from "klisi
  // mishandled what it was given". Report it on the failure.
  const observed = ledger
    .filter((entry) => entry.identity === peer.identity)
    .map((entry) => `${entry.event}${entry.detail ? `(${entry.detail})` : ''}`)
    .join(' → ');
  expect(
    gap,
    `${actor.name} never observed ${gap}; observed for ${peer.name}: [${observed || 'nothing'}]`
  ).toBeUndefined();
}
