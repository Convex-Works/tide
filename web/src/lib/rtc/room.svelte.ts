import {
  ConnectionState,
  DisconnectReason,
  ParticipantKind,
  Room,
  RoomEvent,
  Track,
  TrackPublication,
  type Participant,
  type RemoteParticipant,
  type RemoteTrackPublication
} from 'livekit-client';
import {
  playNewMessageSound,
  playParticipantEnteredSound,
  playParticipantExitedSound,
  playRecordingStartedSound
} from '$lib/sounds';
import { setConnectionChrome } from './connection.svelte';
import { projectParticipants, type MediaSubscriptionFailure, type ParticipantView } from './media';

export type { MediaSubscriptionFailure, MediaTrackView, ParticipantView } from './media';

export type DeviceKind = 'audioinput' | 'videoinput' | 'audiooutput';

export interface DeviceLists {
  audioinput: MediaDeviceInfo[];
  videoinput: MediaDeviceInfo[];
  audiooutput: MediaDeviceInfo[];
}

export interface ActiveDeviceIds {
  audioinput: string;
  videoinput: string;
  audiooutput: string;
}

export interface JoinMediaOptions {
  micEnabled: boolean;
  camEnabled: boolean;
  videoDeviceId?: string;
  audioDeviceId?: string;
}

export interface PreJoinOptions extends JoinMediaOptions {
  name: string;
  videoDeviceId: string;
  audioDeviceId: string;
}

export interface ChatMessage {
  from: string;
  text: string;
  ts: number;
  mine: boolean;
}

// The payload deliberately carries no sender name: attribution comes only
// from the authenticated LiveKit participant, so a guest cannot claim to be
// someone else (chat impersonation, review finding #7).
interface ChatPayload {
  text: string;
  ts: number;
}

const maximumChatMessages = 200;
const subscriptionRetryDelay = 250;
const removalReconcileDelay = 100;

interface MediaProbeParticipant {
  identity: string;
  name: string;
  isLocal: boolean;
  publications: Record<
    string,
    Omit<ParticipantView['publications'][string], 'track'> & {
      track?: { id: string; readyState: MediaStreamTrackState };
    }
  >;
}

export class RoomState {
  // adaptiveStream is intentionally OFF: it pauses remote video layers based
  // on the observed element size, and tiles measured mid-layout (0×0) could
  // stay paused — the "gray tile on join" bug. klisi meetings are small by
  // design, so we always subscribe to the full stream; dynacast still saves
  // publisher-side layers.
  readonly room = new Room({ adaptiveStream: false, dynacast: true });

  connectionState = $state<ConnectionState>(ConnectionState.Disconnected);
  participants = $state<ParticipantView[]>([]);
  micEnabled = $state(false);
  camEnabled = $state(false);
  screenShareEnabled = $state(false);
  isRecording = $state(false);
  activeSpeakerIdentities = $state<string[]>([]);
  chat = $state<ChatMessage[]>([]);
  chatRevision = $state(0);
  disconnectReason = $state<DisconnectReason>();
  wasRemoved = $state(false);
  canPlaybackAudio = $state(true);
  canPlaybackVideo = $state(true);
  mediaResumePending = $state(false);
  mediaPlaybackError = $state('');
  subscriptionFailures = $state<Record<string, MediaSubscriptionFailure>>({});
  subscriptionRetryTotals = $state<Record<string, number>>({});
  devices = $state<DeviceLists>({ audioinput: [], videoinput: [], audiooutput: [] });
  activeDeviceIds = $state<ActiveDeviceIds>({
    audioinput: '',
    videoinput: '',
    audiooutput: ''
  });
  private hasSyncedRoomMetadata = false;
  private subscriptionRetryAttempts = new Map<string, number>();
  private subscriptionRetryTimers = new Map<string, ReturnType<typeof setTimeout>>();
  private removalReconcileTimers = new Map<string, ReturnType<typeof setTimeout>>();

  constructor() {
    this.room
      .on(RoomEvent.ConnectionStateChanged, (state: ConnectionState) => {
        this.connectionState = state;
        this.syncConnectionChrome(state);
      })
      .on(RoomEvent.Disconnected, (reason?: DisconnectReason) => this.handleDisconnected(reason))
      .on(RoomEvent.Reconnected, () => this.reconcileMedia(true))
      .on(RoomEvent.ParticipantConnected, (participant: RemoteParticipant) => {
        this.reconcileMedia();
        if (participant.kind !== ParticipantKind.EGRESS) playParticipantEnteredSound();
      })
      .on(RoomEvent.ParticipantDisconnected, (participant: RemoteParticipant) => {
        // LiveKit can emit teardown callbacks just before it flips the room
        // into Reconnecting. Debounce destructive projections so that a full
        // PC restart never empties the keyed DOM; Reconnected provides the
        // authoritative snapshot.
        this.scheduleRemovalReconcile(`participant:${participant.identity}`);
        if (participant.kind === ParticipantKind.EGRESS) return;
        setTimeout(() => {
          if (
            this.connectionState === ConnectionState.Connected &&
            !this.room.remoteParticipants.has(participant.identity)
          ) {
            playParticipantExitedSound();
          }
        }, removalReconcileDelay);
      })
      .on(RoomEvent.TrackPublished, () => this.reconcileMedia())
      .on(RoomEvent.TrackUnpublished, (publication) => {
        this.clearSubscriptionFailure(publication.trackSid);
        this.scheduleRemovalReconcile(`publication:${publication.trackSid}`);
      })
      .on(RoomEvent.TrackSubscribed, (_track, publication) => {
        this.clearSubscriptionFailure(publication.trackSid);
        this.reconcileMedia();
      })
      .on(RoomEvent.TrackSubscriptionFailed, (publicationSid, _participant, reason) => {
        this.recordSubscriptionFailure(publicationSid, reason);
      })
      .on(RoomEvent.TrackUnsubscribed, (_track, publication) => {
        this.scheduleRemovalReconcile(`subscription:${publication.trackSid}`);
      })
      .on(RoomEvent.TrackMuted, () => this.syncAllMediaState())
      .on(RoomEvent.TrackUnmuted, () => this.syncAllMediaState())
      .on(RoomEvent.TrackStreamStateChanged, () => this.reconcileMedia())
      .on(RoomEvent.TrackSubscriptionStatusChanged, (publication) => {
        this.scheduleRemovalReconcile(`subscription-status:${publication.trackSid}`);
      })
      .on(RoomEvent.TrackSubscriptionPermissionChanged, (publication, status) => {
        if (status === TrackPublication.PermissionStatus.NotAllowed) {
          this.cancelSubscriptionRetry(publication.trackSid);
          const failure = this.subscriptionFailures[publication.trackSid];
          if (failure) {
            this.subscriptionFailures = {
              ...this.subscriptionFailures,
              [publication.trackSid]: {
                ...failure,
                message: 'Permission to subscribe to this media publication was denied.',
                recoverable: false,
                retrying: false,
                exhausted: true
              }
            };
          }
        } else {
          const failure = this.subscriptionFailures[publication.trackSid];
          if (failure && !failure.recoverable) this.clearSubscriptionFailure(publication.trackSid);
        }
        this.reconcileMedia();
      })
      .on(RoomEvent.LocalTrackPublished, () => this.syncAllMediaState())
      .on(RoomEvent.LocalTrackUnpublished, () => this.syncAllMediaState())
      .on(RoomEvent.ParticipantMetadataChanged, () => this.reconcileMedia())
      .on(RoomEvent.ParticipantNameChanged, () => this.reconcileMedia())
      .on(RoomEvent.ParticipantAttributesChanged, () => this.reconcileMedia())
      .on(RoomEvent.ParticipantPermissionsChanged, () => this.reconcileMedia())
      .on(RoomEvent.ParticipantActive, () => this.reconcileMedia())
      .on(RoomEvent.RoomMetadataChanged, () => this.syncRoomMetadata())
      .on(RoomEvent.AudioPlaybackStatusChanged, (playing) => {
        this.canPlaybackAudio = playing;
        if (playing && this.canPlaybackVideo) this.mediaPlaybackError = '';
      })
      .on(RoomEvent.VideoPlaybackStatusChanged, (playing) => {
        this.canPlaybackVideo = playing;
        if (playing && this.canPlaybackAudio) this.mediaPlaybackError = '';
      })
      .on(
        RoomEvent.DataReceived,
        (payload: Uint8Array, participant?: RemoteParticipant, _kind?: unknown, topic?: string) => {
          if (topic === 'chat') this.receiveChat(payload, participant);
        }
      )
      // Speaker changes fire continuously during speech; they must NOT rebuild
      // the participants array (that churns every tile's media element).
      // Tiles derive their speaking highlight from activeSpeakerIdentities.
      .on(RoomEvent.ActiveSpeakersChanged, (speakers: Participant[]) => {
        this.activeSpeakerIdentities = speakers.map((speaker) => speaker.identity);
      })
      .on(RoomEvent.MediaDevicesChanged, () => {
        void this.refreshDevices().catch(() => undefined);
      });

    if (
      (import.meta.env.DEV || import.meta.env.VITE_KLISI_TEST === 'true') &&
      typeof window !== 'undefined'
    ) {
      (window as Window & { __klisiRoom?: Room }).__klisiRoom = this.room;
    }
    this.installMediaTestHooks();
  }

  async connect(wsURL: string, token: string, media: JoinMediaOptions): Promise<void> {
    this.chat = [];
    this.chatRevision = 0;
    this.hasSyncedRoomMetadata = false;
    this.disconnectReason = undefined;
    this.wasRemoved = false;
    this.mediaPlaybackError = '';
    this.subscriptionFailures = {};
    try {
      await this.room.connect(wsURL, token);
      this.connectionState = this.room.state;
      this.canPlaybackAudio = this.room.canPlaybackAudio;
      this.canPlaybackVideo = this.room.canPlaybackVideo;
      this.syncRoomMetadata();

      if (media.audioDeviceId) {
        await this.switchDevice('audioinput', media.audioDeviceId);
      }
      if (media.videoDeviceId) {
        await this.switchDevice('videoinput', media.videoDeviceId);
      }

      await Promise.all([
        this.room.localParticipant.setMicrophoneEnabled(media.micEnabled),
        this.room.localParticipant.setCameraEnabled(media.camEnabled)
      ]);
      this.syncAllMediaState();
      await this.refreshDevices().catch(() => undefined);
      playParticipantEnteredSound();
    } catch (error) {
      await this.leave().catch(() => undefined);
      throw error;
    }
  }

  async toggleMic(): Promise<void> {
    await this.room.localParticipant.setMicrophoneEnabled(!this.micEnabled);
    this.syncAllMediaState();
  }

  async toggleCam(): Promise<void> {
    await this.room.localParticipant.setCameraEnabled(!this.camEnabled);
    this.syncAllMediaState();
  }

  async toggleScreenShare(): Promise<void> {
    await this.room.localParticipant.setScreenShareEnabled(!this.screenShareEnabled);
    this.syncAllMediaState();
  }

  async resumeMedia(): Promise<void> {
    this.mediaResumePending = true;
    this.mediaPlaybackError = '';

    // Both calls are created synchronously in the click stack so browsers see
    // the user activation. Awaiting one before starting the other loses it.
    const starts: Promise<void>[] = [];
    if (!this.canPlaybackAudio) starts.push(this.room.startAudio());
    if (!this.canPlaybackVideo) starts.push(this.room.startVideo());
    this.retryExhaustedSubscriptions();

    const results = await Promise.allSettled(starts);
    this.canPlaybackAudio = this.room.canPlaybackAudio;
    this.canPlaybackVideo = this.room.canPlaybackVideo;
    const rejected = results.find(
      (result): result is PromiseRejectedResult => result.status === 'rejected'
    );
    if (rejected || !this.canPlaybackAudio || !this.canPlaybackVideo) {
      this.mediaPlaybackError =
        rejected?.reason instanceof Error
          ? rejected.reason.message
          : 'Playback is still blocked. Check this site’s media permissions and try again.';
    }
    this.mediaResumePending = false;
  }

  async sendChat(text: string): Promise<void> {
    const message = text.trim();
    if (!message) return;
    const payload: ChatPayload = {
      text: message,
      ts: Date.now()
    };
    await this.room.localParticipant.publishData(
      new TextEncoder().encode(JSON.stringify(payload)),
      { reliable: true, topic: 'chat' }
    );
    this.appendChat({
      from: this.room.localParticipant.name || this.room.localParticipant.identity,
      text: payload.text,
      ts: payload.ts,
      mine: true
    });
  }

  isHostParticipant(participant: ParticipantView): boolean {
    return participant.role === 'host';
  }

  async switchDevice(kind: DeviceKind, deviceId: string): Promise<void> {
    const switched = await this.room.switchActiveDevice(kind, deviceId);
    if (!switched) {
      throw new Error('Could not switch media device.');
    }
    this.activeDeviceIds = { ...this.activeDeviceIds, [kind]: deviceId };
  }

  async refreshDevices(): Promise<void> {
    const available = await Room.getLocalDevices(undefined, false);
    this.devices = {
      audioinput: available.filter((device) => device.kind === 'audioinput'),
      videoinput: available.filter((device) => device.kind === 'videoinput'),
      audiooutput: available.filter((device) => device.kind === 'audiooutput')
    };
    this.activeDeviceIds = {
      audioinput: this.room.getActiveDevice('audioinput') ?? '',
      videoinput: this.room.getActiveDevice('videoinput') ?? '',
      audiooutput: this.room.getActiveDevice('audiooutput') ?? ''
    };
  }

  async leave(): Promise<void> {
    await this.room.disconnect();
    this.handleDisconnected(DisconnectReason.CLIENT_INITIATED);
  }

  async disconnect(): Promise<void> {
    await this.leave();
  }

  reconcileMedia(authoritative = false): void {
    if (
      !authoritative &&
      (this.connectionState === ConnectionState.Reconnecting ||
        this.connectionState === ConnectionState.SignalReconnecting)
    ) {
      return;
    }
    const participants: Participant[] = [
      this.room.localParticipant,
      ...this.room.remoteParticipants.values()
    ];
    if (authoritative) this.pruneSubscriptionFailures(participants);
    this.participants = projectParticipants(participants, this.subscriptionFailures);
  }

  private syncAllMediaState(): void {
    this.micEnabled = this.room.localParticipant.isMicrophoneEnabled;
    this.camEnabled = this.room.localParticipant.isCameraEnabled;
    this.screenShareEnabled = this.room.localParticipant.isScreenShareEnabled;
    this.reconcileMedia();
  }

  private handleDisconnected(reason?: DisconnectReason): void {
    this.disconnectReason = reason;
    this.wasRemoved = reason === DisconnectReason.PARTICIPANT_REMOVED;
    this.connectionState = ConnectionState.Disconnected;
    this.participants = [];
    this.activeSpeakerIdentities = [];
    this.micEnabled = false;
    this.camEnabled = false;
    this.screenShareEnabled = false;
    this.isRecording = false;
    this.hasSyncedRoomMetadata = false;
    this.canPlaybackAudio = true;
    this.canPlaybackVideo = true;
    this.mediaResumePending = false;
    this.mediaPlaybackError = '';
    this.subscriptionFailures = {};
    this.subscriptionRetryAttempts.clear();
    for (const timer of this.subscriptionRetryTimers.values()) clearTimeout(timer);
    this.subscriptionRetryTimers.clear();
    for (const timer of this.removalReconcileTimers.values()) clearTimeout(timer);
    this.removalReconcileTimers.clear();
    setConnectionChrome('offline');
  }

  private scheduleRemovalReconcile(key: string): void {
    const existing = this.removalReconcileTimers.get(key);
    if (existing) clearTimeout(existing);
    const timer = setTimeout(() => {
      this.removalReconcileTimers.delete(key);
      if (this.connectionState === ConnectionState.Connected) this.reconcileMedia(true);
    }, removalReconcileDelay);
    this.removalReconcileTimers.set(key, timer);
  }

  private recordSubscriptionFailure(publicationSid: string, reason?: unknown): void {
    const publication = this.remotePublication(publicationSid);
    const attempts = this.subscriptionRetryAttempts.get(publicationSid) ?? 0;
    const recoverable = publication ? this.canRetryPublication(publication) : false;
    const failure: MediaSubscriptionFailure = {
      publicationSid,
      message:
        reason === undefined
          ? 'Could not subscribe to this media publication.'
          : `Could not subscribe to this media publication (LiveKit error ${String(reason)}).`,
      recoverable,
      retrying: false,
      exhausted: !recoverable || attempts >= 1,
      attempts,
      occurredAt: Date.now()
    };
    this.subscriptionFailures = {
      ...this.subscriptionFailures,
      [publicationSid]: failure
    };
    this.reconcileMedia();
    if (recoverable && attempts < 1) this.scheduleSubscriptionRetry(publicationSid);
  }

  private scheduleSubscriptionRetry(publicationSid: string): void {
    if (this.subscriptionRetryTimers.has(publicationSid)) return;
    const timer = setTimeout(() => {
      this.subscriptionRetryTimers.delete(publicationSid);
      const publication = this.remotePublication(publicationSid);
      if (!publication || !this.canRetryPublication(publication)) return;

      const attempts = (this.subscriptionRetryAttempts.get(publicationSid) ?? 0) + 1;
      this.subscriptionRetryAttempts.set(publicationSid, attempts);
      this.subscriptionRetryTotals = {
        ...this.subscriptionRetryTotals,
        [publicationSid]: (this.subscriptionRetryTotals[publicationSid] ?? 0) + 1
      };
      const failure = this.subscriptionFailures[publicationSid];
      if (failure) {
        this.subscriptionFailures = {
          ...this.subscriptionFailures,
          [publicationSid]: {
            ...failure,
            attempts,
            retrying: true,
            exhausted: false
          }
        };
      }

      // Toggle the desired state to force a fresh subscription request. This
      // cycle is bounded by subscriptionRetryAttempts until TrackSubscribed
      // clears the failure episode.
      publication.setSubscribed(false);
      publication.setSubscribed(true);
      this.reconcileMedia();
    }, subscriptionRetryDelay);
    this.subscriptionRetryTimers.set(publicationSid, timer);
  }

  private retryExhaustedSubscriptions(): void {
    for (const failure of Object.values(this.subscriptionFailures)) {
      if (!failure.recoverable || !failure.exhausted) continue;
      const publication = this.remotePublication(failure.publicationSid);
      if (!publication || !this.canRetryPublication(publication)) continue;
      this.subscriptionRetryTotals = {
        ...this.subscriptionRetryTotals,
        [failure.publicationSid]: (this.subscriptionRetryTotals[failure.publicationSid] ?? 0) + 1
      };
      this.subscriptionFailures = {
        ...this.subscriptionFailures,
        [failure.publicationSid]: {
          ...failure,
          retrying: true,
          exhausted: false,
          occurredAt: Date.now()
        }
      };
      publication.setSubscribed(false);
      publication.setSubscribed(true);
    }
    this.reconcileMedia();
  }

  private canRetryPublication(publication: RemoteTrackPublication): boolean {
    return (
      this.connectionState === ConnectionState.Connected &&
      publication.isDesired &&
      publication.permissionStatus === TrackPublication.PermissionStatus.Allowed &&
      this.remotePublication(publication.trackSid) === publication
    );
  }

  private remotePublication(publicationSid: string): RemoteTrackPublication | undefined {
    for (const participant of this.room.remoteParticipants.values()) {
      const publication = participant.trackPublications.get(publicationSid);
      if (publication) return publication as RemoteTrackPublication;
    }
    return undefined;
  }

  private cancelSubscriptionRetry(publicationSid: string): void {
    const timer = this.subscriptionRetryTimers.get(publicationSid);
    if (timer) clearTimeout(timer);
    this.subscriptionRetryTimers.delete(publicationSid);
  }

  private clearSubscriptionFailure(publicationSid: string): void {
    this.cancelSubscriptionRetry(publicationSid);
    this.subscriptionRetryAttempts.delete(publicationSid);
    if (!(publicationSid in this.subscriptionFailures)) return;
    const next = { ...this.subscriptionFailures };
    delete next[publicationSid];
    this.subscriptionFailures = next;
  }

  private pruneSubscriptionFailures(participants: Participant[]): void {
    const published = new Set(
      participants.flatMap((participant) => [...participant.trackPublications.keys()])
    );
    for (const publicationSid of Object.keys(this.subscriptionFailures)) {
      if (!published.has(publicationSid)) this.clearSubscriptionFailure(publicationSid);
    }
  }

  async mediaProbeSnapshot(): Promise<{
    connectionState: ConnectionState;
    playback: { audio: boolean; video: boolean; error: string };
    participants: MediaProbeParticipant[];
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
    subscriptionFailures: Record<string, MediaSubscriptionFailure>;
    subscriptionRetryTotals: Record<string, number>;
  }> {
    const probeParticipants = projectParticipants(
      [this.room.localParticipant, ...this.room.remoteParticipants.values()],
      this.subscriptionFailures
    );
    const elements =
      typeof document === 'undefined'
        ? []
        : [
            ...document.querySelectorAll<HTMLMediaElement>(
              'audio[data-publication-sid], video[data-publication-sid]'
            )
          ].map((element) => {
            const sid = element.dataset.publicationSid ?? '';
            const publication = probeParticipants
              .flatMap((participant) => Object.values(participant.publications))
              .find((candidate) => candidate.publicationSid === sid);
            const stream = element.srcObject;
            return {
              publicationSid: sid,
              source: element.dataset.trackSource ?? '',
              kind: element.tagName.toLowerCase(),
              srcObjectTrackIds:
                stream instanceof MediaStream ? stream.getTracks().map((track) => track.id) : [],
              attachedInLiveKit: publication?.track?.attachedElements.includes(element) === true
            };
          });
    const inbound: Record<
      string,
      {
        bytesReceived: number;
        framesDecoded: number;
        packetsLost: number;
        jitter: number;
        audioLevel: number;
        totalAudioEnergy: number;
      }
    > = {};

    for (const participant of probeParticipants) {
      if (participant.isLocal) continue;
      for (const publication of Object.values(participant.publications)) {
        const track = publication.track as
          (Track & { getRTCStatsReport?: () => Promise<RTCStatsReport | undefined> }) | undefined;
        const report = await track?.getRTCStatsReport?.();
        const totals = {
          bytesReceived: 0,
          framesDecoded: 0,
          packetsLost: 0,
          jitter: 0,
          audioLevel: 0,
          totalAudioEnergy: 0
        };
        report?.forEach((stat) => {
          if (stat.type !== 'inbound-rtp') return;
          const inboundStat = stat as RTCInboundRtpStreamStats & {
            framesDecoded?: number;
            audioLevel?: number;
            totalAudioEnergy?: number;
          };
          totals.bytesReceived += inboundStat.bytesReceived ?? 0;
          totals.framesDecoded += inboundStat.framesDecoded ?? 0;
          totals.packetsLost += inboundStat.packetsLost ?? 0;
          totals.jitter = Math.max(totals.jitter, inboundStat.jitter ?? 0);
          totals.audioLevel = Math.max(totals.audioLevel, inboundStat.audioLevel ?? 0);
          totals.totalAudioEnergy += inboundStat.totalAudioEnergy ?? 0;
        });
        inbound[publication.publicationSid] = totals;
      }
    }

    return {
      connectionState: this.connectionState,
      playback: {
        audio: this.canPlaybackAudio,
        video: this.canPlaybackVideo,
        error: this.mediaPlaybackError
      },
      participants: probeParticipants.map((participant) => ({
        identity: participant.identity,
        name: participant.name,
        isLocal: participant.isLocal,
        publications: Object.fromEntries(
          Object.values(participant.publications).map((publication) => [
            publication.publicationSid,
            {
              ...publication,
              track: publication.track
                ? {
                    id: publication.track.mediaStreamTrack.id,
                    readyState: publication.track.mediaStreamTrack.readyState
                  }
                : undefined
            }
          ])
        )
      })),
      elements,
      inbound,
      subscriptionFailures: this.subscriptionFailures,
      subscriptionRetryTotals: this.subscriptionRetryTotals
    };
  }

  private installMediaTestHooks(): void {
    if (import.meta.env.VITE_KLISI_TEST !== 'true' || typeof window === 'undefined') return;
    type PlaybackKind = 'audio' | 'video' | 'any';
    type TestAPI = {
      attachmentDelayMs: number;
      snapshot: () => ReturnType<RoomState['mediaProbeSnapshot']>;
      reconcile: () => void;
      clearSrcObject: (publicationSid: string) => boolean;
      forgetLiveKitAttachment: (publicationSid: string) => boolean;
      injectSubscriptionFailure: (publicationSid: string, exhausted?: boolean) => void;
      rejectNextPlayback: (kind?: PlaybackKind) => void;
    };
    const testWindow = window as Window & { __klisiMediaTest?: TestAPI };
    let rejectedKind: PlaybackKind | undefined;
    const nativePlay = HTMLMediaElement.prototype.play;
    HTMLMediaElement.prototype.play = function (): Promise<void> {
      const kind: PlaybackKind = this instanceof HTMLAudioElement ? 'audio' : 'video';
      if (rejectedKind === 'any' || rejectedKind === kind) {
        rejectedKind = undefined;
        return Promise.reject(
          new DOMException('Playback blocked by Klisi test hook.', 'NotAllowedError')
        );
      }
      return nativePlay.call(this);
    };

    testWindow.__klisiMediaTest = {
      attachmentDelayMs: 0,
      snapshot: () => this.mediaProbeSnapshot(),
      reconcile: () => this.reconcileMedia(true),
      clearSrcObject: (publicationSid) => {
        const element = document.querySelector<HTMLMediaElement>(
          `[data-publication-sid="${CSS.escape(publicationSid)}"]`
        );
        if (!element) return false;
        element.srcObject = null;
        return true;
      },
      forgetLiveKitAttachment: (publicationSid) => {
        const element = document.querySelector<HTMLMediaElement>(
          `[data-publication-sid="${CSS.escape(publicationSid)}"]`
        );
        const publication = this.participants
          .flatMap((participant) => Object.values(participant.publications))
          .find((candidate) => candidate.publicationSid === publicationSid);
        if (!element || !publication?.track) return false;
        const index = publication.track.attachedElements.indexOf(element);
        if (index < 0) return false;
        publication.track.attachedElements.splice(index, 1);
        return true;
      },
      injectSubscriptionFailure: (publicationSid, exhausted = false) => {
        if (exhausted) this.subscriptionRetryAttempts.set(publicationSid, 1);
        this.recordSubscriptionFailure(publicationSid);
      },
      rejectNextPlayback: (kind = 'any') => {
        rejectedKind = kind;
      }
    };
  }

  private syncRoomMetadata(): void {
    let recording = false;
    if (this.room.metadata) {
      try {
        const metadata = JSON.parse(this.room.metadata) as { recording?: unknown };
        recording = metadata.recording === true;
      } catch {
        recording = false;
      }
    }
    const recordingStarted = this.hasSyncedRoomMetadata && recording && !this.isRecording;
    this.isRecording = recording;
    this.hasSyncedRoomMetadata = true;
    if (recordingStarted) playRecordingStartedSound();
    this.syncConnectionChrome(this.connectionState);
  }

  private receiveChat(payload: Uint8Array, participant?: RemoteParticipant): void {
    try {
      const decoded = JSON.parse(new TextDecoder().decode(payload)) as Partial<ChatPayload>;
      if (
        typeof decoded.text !== 'string' ||
        typeof decoded.ts !== 'number' ||
        decoded.text.trim() === ''
      ) {
        return;
      }
      this.appendChat({
        from: participant?.name || participant?.identity || 'Guest',
        text: decoded.text,
        ts: decoded.ts,
        mine: participant?.identity === this.room.localParticipant.identity
      });
    } catch {
      // Ignore data messages that are not valid klisi chat payloads.
    }
  }

  private appendChat(message: ChatMessage): void {
    this.chat = [...this.chat, message].slice(-maximumChatMessages);
    this.chatRevision += 1;
    if (!message.mine) playNewMessageSound();
  }

  private syncConnectionChrome(state: ConnectionState): void {
    if (state === ConnectionState.Reconnecting || state === ConnectionState.SignalReconnecting) {
      setConnectionChrome('reconnecting');
    } else if (state === ConnectionState.Disconnected) {
      setConnectionChrome('offline');
    } else if (this.isRecording) {
      setConnectionChrome('recording');
    } else {
      setConnectionChrome('connected');
    }
  }
}
