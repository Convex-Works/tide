import {
  Track,
  TrackPublication,
  type Participant,
  type RemoteTrackPublication
} from 'livekit-client';

export interface MediaSubscriptionFailure {
  publicationSid: string;
  message: string;
  recoverable: boolean;
  retrying: boolean;
  exhausted: boolean;
  attempts: number;
  occurredAt: number;
}

export interface MediaTrackView {
  publicationSid: string;
  source: Track.Source;
  kind: Track.Kind;
  track?: Track;
  muted: boolean;
  subscribed: boolean;
  desired: boolean;
  permissionAllowed: boolean;
  subscriptionStatus: TrackPublication.SubscriptionStatus;
  streamState: Track.StreamState;
  failure?: MediaSubscriptionFailure;
}

export interface ParticipantView {
  identity: string;
  name: string;
  isLocal: boolean;
  role?: 'host';
  publications: Record<string, MediaTrackView>;
  cameras: Record<string, MediaTrackView>;
  screenShares: Record<string, MediaTrackView>;
  audio: Record<string, MediaTrackView>;
  camera?: MediaTrackView;
  screenShare?: MediaTrackView;
  microphone?: MediaTrackView;
  micMuted: boolean;
  camMuted: boolean;
}

export function projectParticipants(
  participants: Participant[],
  failures: Readonly<Record<string, MediaSubscriptionFailure>> = {}
): ParticipantView[] {
  return participants.map((participant) => projectParticipant(participant, failures));
}

export function projectParticipant(
  participant: Participant,
  failures: Readonly<Record<string, MediaSubscriptionFailure>> = {}
): ParticipantView {
  const isLocal = participant.isLocal;
  const publicationList = [...participant.trackPublications.values()];
  const publications = Object.fromEntries(
    publicationList.map((publication) => {
      const remote = isLocal ? undefined : (publication as RemoteTrackPublication);
      const track = publication.track;
      const view: MediaTrackView = {
        publicationSid: publication.trackSid,
        source: publication.source,
        kind: publication.kind,
        track,
        muted: publication.isMuted,
        subscribed: isLocal ? Boolean(track) : remote?.isSubscribed === true,
        desired: isLocal ? true : remote?.isDesired === true,
        permissionAllowed:
          isLocal || remote?.permissionStatus !== TrackPublication.PermissionStatus.NotAllowed,
        subscriptionStatus: isLocal
          ? track
            ? TrackPublication.SubscriptionStatus.Subscribed
            : TrackPublication.SubscriptionStatus.Unsubscribed
          : (remote?.subscriptionStatus ?? TrackPublication.SubscriptionStatus.Unsubscribed),
        // Paused publications deliberately remain projected and attached. LiveKit
        // will resume them when the SFU has bandwidth again.
        streamState: track?.streamState ?? Track.StreamState.Unknown,
        failure: failures[publication.trackSid]
      };
      return [publication.trackSid, view];
    })
  );
  const values = Object.values(publications);
  const cameras = recordsFor(values, Track.Source.Camera);
  const screenShares = recordsFor(values, Track.Source.ScreenShare);
  const audio = Object.fromEntries(
    values
      .filter((publication) => publication.kind === Track.Kind.Audio)
      .map((publication) => [publication.publicationSid, publication])
  );
  const microphone = newest(values.filter((view) => view.source === Track.Source.Microphone));

  return {
    identity: participant.identity,
    name: participant.name || participant.identity,
    isLocal,
    role: participantRole(participant),
    publications,
    cameras,
    screenShares,
    audio,
    camera: newestPlayable(Object.values(cameras)),
    screenShare: newestPlayable(Object.values(screenShares)),
    microphone,
    micMuted: !microphone || microphone.muted,
    camMuted:
      Object.keys(cameras).length === 0 || Object.values(cameras).every((camera) => camera.muted)
  };
}

export function isPlayable(view: MediaTrackView): view is MediaTrackView & { track: Track } {
  return !view.muted && view.subscribed && view.permissionAllowed && view.track !== undefined;
}

function recordsFor(
  publications: MediaTrackView[],
  source: Track.Source
): Record<string, MediaTrackView> {
  return Object.fromEntries(
    publications
      .filter((publication) => publication.source === source)
      .map((publication) => [publication.publicationSid, publication])
  );
}

function newest(publications: MediaTrackView[]): MediaTrackView | undefined {
  return publications.at(-1);
}

function newestPlayable(publications: MediaTrackView[]): MediaTrackView | undefined {
  const playable = publications.filter(isPlayable);
  return (
    playable
      .filter(
        (publication) =>
          publication.streamState !== Track.StreamState.Paused &&
          publication.track.mediaStreamTrack.readyState === 'live'
      )
      .at(-1) ?? playable.at(-1)
  );
}

function participantRole(participant: Participant): 'host' | undefined {
  if (!participant.metadata) return undefined;
  try {
    const metadata = JSON.parse(participant.metadata) as { role?: unknown };
    return metadata.role === 'host' ? 'host' : undefined;
  } catch {
    return undefined;
  }
}
