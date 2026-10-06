import {
  ConnectionQuality,
  ConnectionState,
  DisconnectReason,
  ParticipantKind,
  Room,
  RoomEvent,
  Track,
  TrackPublication,
  type Participant,
  type RemoteParticipant,
  type RemoteTrackPublication,
  type RoomEventCallbacks
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
const participantExitSoundDelay = 100;
const maximumLedgerEntries = 500;
const maximumHandlerFaults = 50;

// The floor under every dropped event, including the ones neither we nor
// LiveKit have thought of. Slow enough to be free on an idle meeting (the
// projection is skipped when nothing changed), fast enough that a missed
// event is a blink rather than a reload.
const projectionHeartbeatInterval = 2_000;

/** What this client observed LiveKit emit. Bounded, local, never auto-shipped. */
export interface MediaLedgerEntry {
  at: number;
  event: string;
  identity?: string;
  publicationSid?: string;
  detail?: string;
}

/** A listener that threw. Kept so the next incident is attributable. */
export interface HandlerFault {
  at: number;
  event: string;
  message: string;
}

interface MediaProbeParticipant {
  identity: string;
  name: string;
  isLocal: boolean;
  // Recording adds an egress participant that subscribes but never publishes.
  // Mesh assertions compare exact participant sets, so it must be separable.
  isEgress: boolean;
  publications: Record<
    string,
    Omit<ParticipantView['publications'][string], 'track'> & {
      track?: { id: string; readyState: MediaStreamTrackState };
    }
  >;
}

/**
 * Everything the projection depends on, flattened. Two reconciles with the
 * same signature would produce an identical `ParticipantView[]`, so the
 * heartbeat can skip republishing one and leave the DOM alone.
 */
function projectionSignature(views: ParticipantView[]): string {
  return views
    .map((view) =>
      [
        view.identity,
        view.name,
        view.role ?? '',
        ...Object.values(view.publications).map((publication) =>
          [
            publication.publicationSid,
            publication.source,
            publication.kind,
            publication.muted ? 'muted' : '',
            publication.subscribed ? 'subscribed' : '',
            publication.desired ? 'desired' : '',
            publication.permissionAllowed ? 'allowed' : '',
            publication.subscriptionStatus,
            publication.streamState,
            publication.track?.mediaStreamTrack.id ?? '',
            publication.failure ? `failed:${publication.failure.recoverable}` : ''
          ].join(':')
        )
      ].join('|')
    )
    .join('\n');
}

export class RoomState {
  // Both layer-pausing optimisations are OFF, for one reason: a paused video
  // layer that never resumes is indistinguishable from a broken one, and tide
  // meetings are small by design, so neither saves anything worth that risk.
  //
  // adaptiveStream pauses *remote* layers by observed element size, and tiles
  // measured mid-layout (0×0) could stay paused — the "gray tile on join" bug.
  //
  // dynacast pauses *publisher* layers when the last subscriber to them drops,
  // which is exactly what a reload does. Runs 333 and 334 caught the resume
  // going missing: after a reload the peer's camera sat at subscribed,
  // streamState active, track live, element attached — and zero bytesReceived
  // with zero packetsLost for 25 s, while that same peer's audio flowed
  // normally. Video-only, publisher-side, never recovering: a layer that was
  // paused when the subscriber left and not resumed when it came back.
  readonly room = new Room({ adaptiveStream: false, dynacast: false });

  connectionState = $state<ConnectionState>(ConnectionState.Disconnected);
  participants = $state<ParticipantView[]>([]);
  hiddenCameraIdentities = $state<string[]>([]);
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
  mediaPlaybackError = $state('');
  subscriptionFailures = $state<Record<string, MediaSubscriptionFailure>>({});
  devices = $state<DeviceLists>({ audioinput: [], videoinput: [], audiooutput: [] });
  activeDeviceIds = $state<ActiveDeviceIds>({
    audioinput: '',
    videoinput: '',
    audiooutput: ''
  });
  // A seam, not indirection for its own sake. LiveKit installs a participant's
  // track-event forwarding *after* it emits ParticipantConnected, and its
  // emitter does not catch listener exceptions — so a throw in this handler
  // leaves that participant permanently wired to nothing. Media tests replace
  // this field to prove the handler is exception-safe.
  private announceParticipantEntered = (participant: RemoteParticipant): void => {
    if (participant.kind !== ParticipantKind.EGRESS) playParticipantEnteredSound();
  };
  private hasSyncedRoomMetadata = false;
  private mediaPlaybackAttempt = 0;
  private mediaPlaybackUnlock?: AbortController;
  private mediaPlaybackEpoch = 0;
  private mediaPlaybackRecoveryAttempted = false;
  private mediaPlaybackAttemptInFlight = false;
  private projectionDirty = false;
  private projectionAuthoritative = false;
  private projectionFlushQueued = false;
  private projectionSignature = '';
  private projectionTicks = 0;
  private projectionRevisions = 0;
  private projectionHeartbeat?: ReturnType<typeof setInterval>;
  private readonly eventLedger: MediaLedgerEntry[] = [];
  private readonly handlerFaultLog: HandlerFault[] = [];

  constructor() {
    // Before any of tide's own handlers, so the ledger records what LiveKit
    // emitted rather than what a handler decided to do about it.
    this.installEventLedger();

    this.listen(RoomEvent.ConnectionStateChanged, (state) => this.applyConnectionState(state));
    this.listen(RoomEvent.Disconnected, (reason) => this.handleDisconnected(reason));
    this.listen(RoomEvent.Reconnected, () => {
      this.markProjectionDirty(true);
      this.recoverMediaPlayback();
    });
    this.listen(RoomEvent.ParticipantConnected, (participant) => {
      this.markProjectionDirty(true);
      this.deferSideEffect(RoomEvent.ParticipantConnected, () =>
        this.announceParticipantEntered(participant)
      );
    });
    this.listen(RoomEvent.ParticipantDisconnected, (participant) => {
      this.markProjectionDirty(true);
      if (participant.kind === ParticipantKind.EGRESS) return;
      // A full restart disconnects everyone before it reconnects them. Delay
      // the sound past that window so a reconnect is silent, and re-check the
      // room rather than trusting the event.
      setTimeout(() => {
        if (
          this.connectionState === ConnectionState.Connected &&
          !this.room.remoteParticipants.has(participant.identity)
        ) {
          playParticipantExitedSound();
        }
      }, participantExitSoundDelay);
    });
    this.listen(RoomEvent.TrackPublished, () => this.markProjectionDirty());
    this.listen(RoomEvent.TrackUnpublished, (publication) => {
      this.clearSubscriptionFailure(publication.trackSid);
      this.markProjectionDirty(true);
    });
    this.listen(RoomEvent.TrackSubscribed, (_track, publication) => {
      this.clearSubscriptionFailure(publication.trackSid);
      this.markProjectionDirty();
    });
    this.listen(RoomEvent.TrackSubscriptionFailed, (publicationSid, _participant, reason) => {
      this.recordSubscriptionFailure(publicationSid, reason);
    });
    this.listen(RoomEvent.TrackUnsubscribed, () => this.markProjectionDirty(true));
    this.listen(RoomEvent.TrackMuted, () => this.syncAllMediaState());
    this.listen(RoomEvent.TrackUnmuted, () => this.syncAllMediaState());
    this.listen(RoomEvent.TrackStreamStateChanged, () => this.markProjectionDirty());
    this.listen(RoomEvent.TrackSubscriptionStatusChanged, () => this.markProjectionDirty());
    this.listen(RoomEvent.TrackSubscriptionPermissionChanged, (publication, status) => {
      if (status === TrackPublication.PermissionStatus.NotAllowed) {
        this.subscriptionFailures = {
          ...this.subscriptionFailures,
          [publication.trackSid]: {
            publicationSid: publication.trackSid,
            message: 'Permission to subscribe to this media publication was denied.',
            recoverable: false,
            occurredAt: Date.now()
          }
        };
      } else {
        this.clearSubscriptionFailure(publication.trackSid);
      }
      this.markProjectionDirty(true);
    });
    this.listen(RoomEvent.LocalTrackPublished, () => this.syncAllMediaState());
    this.listen(RoomEvent.LocalTrackUnpublished, () => this.syncAllMediaState());
    this.listen(RoomEvent.ParticipantMetadataChanged, () => this.markProjectionDirty());
    this.listen(RoomEvent.ParticipantNameChanged, () => this.markProjectionDirty());
    this.listen(RoomEvent.ParticipantAttributesChanged, () => this.markProjectionDirty());
    this.listen(RoomEvent.ParticipantPermissionsChanged, () => this.markProjectionDirty());
    this.listen(RoomEvent.ParticipantActive, () => this.markProjectionDirty());
    // Quality reaching Lost is what hides a ghost (see connectedParticipants);
    // leaving Lost is what brings a resumed participant back. The heartbeat
    // would catch both within 2 s, but the event makes it immediate.
    this.listen(RoomEvent.ConnectionQualityChanged, () => this.markProjectionDirty());
    this.listen(RoomEvent.RoomMetadataChanged, () => this.syncRoomMetadata());
    this.listen(RoomEvent.AudioPlaybackStatusChanged, (playing) => {
      this.canPlaybackAudio = playing;
      if (playing && this.canPlaybackVideo) {
        this.markPlaybackHealthy();
      } else if (!playing) {
        this.recoverMediaPlayback();
      }
    });
    this.listen(RoomEvent.VideoPlaybackStatusChanged, (playing) => {
      this.canPlaybackVideo = playing;
      if (playing && this.canPlaybackAudio) {
        this.markPlaybackHealthy();
      } else if (!playing) {
        this.recoverMediaPlayback();
      }
    });
    this.listen(RoomEvent.DataReceived, (payload, participant, _kind, topic) => {
      if (topic === 'chat') this.receiveChat(payload, participant);
    });
    // Speaker changes fire continuously during speech; they must NOT rebuild
    // the participants array (that churns every tile's media element).
    // Tiles derive their speaking highlight from activeSpeakerIdentities.
    this.listen(RoomEvent.ActiveSpeakersChanged, (speakers) => {
      this.activeSpeakerIdentities = speakers.map((speaker) => speaker.identity);
    });
    this.listen(RoomEvent.MediaDevicesChanged, () => {
      void this.refreshDevices().catch(() => undefined);
    });

    if (typeof window !== 'undefined') {
      if (import.meta.env.DEV || import.meta.env.VITE_TIDE_TEST === 'true') {
        (window as Window & { __tideRoom?: Room }).__tideRoom = this.room;
      }
      // The support path for a media incident. Deliberately not a UI control:
      // the feature list is frozen, and this is a debug tool. It writes a file
      // on the machine that runs it and sends nothing anywhere.
      (window as Window & { tideDiagnostics?: () => void }).tideDiagnostics = () =>
        this.downloadDiagnostics();
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
    this.mediaPlaybackRecoveryAttempted = false;
    this.mediaPlaybackAttemptInFlight = false;
    this.subscriptionFailures = {};
    try {
      await this.room.connect(wsURL, token);
      this.applyConnectionState(this.room.state);
      this.canPlaybackAudio = this.room.canPlaybackAudio;
      this.canPlaybackVideo = this.room.canPlaybackVideo;
      if (!this.canPlaybackAudio || !this.canPlaybackVideo) this.recoverMediaPlayback();
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

  /** True while the browser is refusing to play media and is waiting for a gesture. */
  get playbackBlocked(): boolean {
    return this.mediaPlaybackError !== '';
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

  isParticipantCameraHidden(identity: string): boolean {
    return this.hiddenCameraIdentities.includes(identity);
  }

  setParticipantCameraHidden(identity: string, hidden: boolean): void {
    if (identity === this.room.localParticipant.identity) return;
    if (hidden === this.isParticipantCameraHidden(identity)) return;
    this.hiddenCameraIdentities = hidden
      ? [...this.hiddenCameraIdentities, identity]
      : this.hiddenCameraIdentities.filter((candidate) => candidate !== identity);
    // The subscription is not changed here. applySubscriptions() derives it
    // from this list on the next tick, in both directions — the un-hide
    // direction used to live only in this setter, so a camera republished
    // after un-hiding was never re-subscribed.
    this.markProjectionDirty(true);
  }

  /**
   * Starts LiveKit's playback unlock while the caller is still inside a user
   * gesture. Calling this before the asynchronous join path preserves the Join
   * click for remote audio that is attached after the room connects.
   */
  activateMediaPlayback(): void {
    this.clearMediaPlaybackUnlock();
    this.mediaPlaybackRecoveryAttempted = false;
    this.startMediaPlaybackAttempt();
  }

  /**
   * At most one automatic attempt per connection, and never one triggered by
   * an attempt already running.
   *
   * livekit-client reports playback status from two independent probes, and
   * `startAudio()` drives both at once: the media elements, and the
   * AudioContext (`acquireAudioContext` emits `AudioPlaybackStatusChanged`
   * whenever the context's running state disagrees with `canPlaybackAudio`).
   * While an attempt is in flight those two disagree by construction — the
   * context resumes and reports healthy, the elements stay blocked and report
   * blocked. Treating either report as a reason to attempt again is an
   * unbounded microtask loop that pins the CPU and starves the page.
   *
   * Retrying cannot help anyway: unblocking autoplay needs a user gesture. So
   * one attempt, then the unlock control and the armed listeners wait for the
   * gesture that can actually work.
   */
  private recoverMediaPlayback(): void {
    // Keep the fallback armed while the automatic attempt is in flight. A
    // real gesture supersedes that attempt instead of being lost to it.
    this.armMediaPlaybackUnlock();
    if (this.mediaPlaybackAttemptInFlight || this.mediaPlaybackRecoveryAttempted) return;
    this.mediaPlaybackRecoveryAttempted = true;
    this.startMediaPlaybackAttempt();
  }

  private startMediaPlaybackAttempt(): void {
    this.mediaPlaybackError = '';
    const epoch = this.mediaPlaybackEpoch;
    const attempt = ++this.mediaPlaybackAttempt;
    this.mediaPlaybackAttemptInFlight = true;

    // Create both promises synchronously so a click/tap activation is visible
    // to both LiveKit playback paths. Nothing here touches subscriptions:
    // unlocking playback is not a reason to renegotiate with the SFU.
    const starts = [this.room.startAudio(), this.room.startVideo()];

    void Promise.allSettled(starts).then((results) => {
      // A newer attempt owns the flag; this one is stale and settles quietly.
      if (epoch !== this.mediaPlaybackEpoch || attempt !== this.mediaPlaybackAttempt) return;
      this.mediaPlaybackAttemptInFlight = false;
      this.canPlaybackAudio = this.room.canPlaybackAudio;
      this.canPlaybackVideo = this.room.canPlaybackVideo;
      const rejected = results.find(
        (result): result is PromiseRejectedResult => result.status === 'rejected'
      );
      if (rejected || !this.canPlaybackAudio || !this.canPlaybackVideo) {
        this.mediaPlaybackError =
          rejected?.reason instanceof Error
            ? rejected.reason.message
            : 'Playback is still blocked by the browser.';
        this.armMediaPlaybackUnlock();
      } else {
        this.markPlaybackHealthy();
      }
    });
  }

  /**
   * Playback is running, so a later block earns a fresh automatic attempt —
   * but only if this report arrived while nothing was being attempted. A
   * "healthy" report raised *during* an attempt is the AudioContext probe
   * disagreeing with the still-blocked media elements (see
   * recoverMediaPlayback); re-arming on that is the loop itself.
   */
  private markPlaybackHealthy(): void {
    this.mediaPlaybackError = '';
    if (!this.mediaPlaybackAttemptInFlight) this.mediaPlaybackRecoveryAttempted = false;
    this.clearMediaPlaybackUnlock();
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

  /**
   * Re-derives everything the UI shows from LiveKit's own maps. Idempotent by
   * construction: it reads state, never an event. `authoritative` republishes
   * the projection even when nothing changed, which is how a DOM-level desync
   * (a cleared srcObject, a forgotten attachment) gets repaired.
   */
  reconcileMedia(authoritative = false): void {
    this.projectionTicks += 1;
    const participants = this.connectedParticipants();
    this.applySubscriptions();
    this.pruneSubscriptionFailures(participants);
    this.pruneHiddenCameraIdentities();

    const projected = projectParticipants(participants, this.subscriptionFailures);
    const signature = projectionSignature(projected);
    // The heartbeat runs whether or not anything changed. Republishing an
    // unchanged projection invalidates every tile and re-runs every media
    // action, so an idle meeting would pay that cost twice a second forever.
    if (!authoritative && signature === this.projectionSignature) return;
    this.projectionSignature = signature;
    this.projectionRevisions += 1;
    this.participants = projected;
  }

  /**
   * The participants a meeting should show. `remoteParticipants` alone is not
   * it: the server keeps an abruptly departed participant (closed tab, dead
   * laptop) through its resume grace window and re-announces them on every
   * reconnect sync, so projecting the raw map renders ghost tiles — the gate
   * watched a closed tab survive several reconnects. A participant whose
   * connection quality is Lost has no connection behind their map entry; on
   * resume their quality changes again and the projection brings them back.
   * Unknown is not Lost — a fresh joiner reports Unknown until the first
   * quality update, and filtering it would blink every arrival.
   */
  private connectedParticipants(): Participant[] {
    return [
      this.room.localParticipant,
      ...[...this.room.remoteParticipants.values()].filter(
        (participant) => participant.connectionQuality !== ConnectionQuality.Lost
      )
    ];
  }

  /**
   * Marks the projection stale. Reconciliation is coalesced onto a microtask
   * so a burst of events costs one pass, and — more importantly — so it
   * observes the SDK's state *after* the synchronous transition that produced
   * the events. handleRestarting() disconnects every participant before it
   * flips the room to Reconnecting; a handler that projected inline would
   * empty the stage, a handler that projects on the microtask sees the freeze.
   */
  private markProjectionDirty(authoritative = false): void {
    this.projectionDirty = true;
    if (authoritative) this.projectionAuthoritative = true;
    if (this.projectionFlushQueued) return;
    this.projectionFlushQueued = true;
    queueMicrotask(() => {
      // Cleared *after* the flush, not before. Reconciling can itself emit
      // events — a subscription change does — and re-entering on another
      // microtask would starve the page rather than converge. Anything raised
      // by the flush waits for the next event or the heartbeat. Nothing
      // legitimate is delayed by this: JS is single-threaded, so the only
      // events that can arrive during a flush are ones the flush caused.
      this.flushProjection();
      this.projectionFlushQueued = false;
    });
  }

  private flushProjection(): void {
    if (!this.projectionDirty) return;
    // Frozen while not connected — see markProjectionDirty. The flag stays
    // set, so the transition back to Connected flushes it. Nothing that was
    // asked for is ever discarded; it is only ever deferred.
    if (this.connectionState !== ConnectionState.Connected) return;
    const authoritative = this.projectionAuthoritative;
    try {
      this.reconcileMedia(authoritative);
    } catch (error) {
      // Stays dirty on purpose: the heartbeat retries rather than losing the
      // update, which is the difference between a blink and a reload.
      this.recordHandlerFault('projection', error);
      return;
    }
    this.projectionDirty = false;
    this.projectionAuthoritative = false;
  }

  private applyConnectionState(state: ConnectionState): void {
    this.connectionState = state;
    this.syncConnectionChrome(state);
    if (state === ConnectionState.Connected) {
      this.startProjectionHeartbeat();
      // Each connection gets one automatic playback attempt; a resume can
      // legitimately need a fresh one.
      this.mediaPlaybackRecoveryAttempted = false;
      // The exact moment reconciliation becomes legal again is the moment to
      // re-derive: a resume flushes LiveKit's buffered events *before* this
      // transition, and Reconnected is not emitted when the state did not
      // actually change.
      this.markProjectionDirty(true);
    } else {
      this.stopProjectionHeartbeat();
    }
  }

  private startProjectionHeartbeat(): void {
    if (this.projectionHeartbeat !== undefined) return;
    this.projectionHeartbeat = setInterval(
      () => this.markProjectionDirty(),
      projectionHeartbeatInterval
    );
  }

  private stopProjectionHeartbeat(): void {
    if (this.projectionHeartbeat === undefined) return;
    clearInterval(this.projectionHeartbeat);
    this.projectionHeartbeat = undefined;
  }

  /**
   * Registers a listener that cannot throw into livekit-client.
   *
   * LiveKit's emitter dispatches through a bare ReflectApply loop with no
   * try/catch, and getOrCreateParticipant emits ParticipantConnected *before*
   * it installs that participant's track-event forwarding. One escaped
   * exception therefore aborts construction after the map insert, and the
   * forwarding is never installed for the rest of the session. Every
   * registration goes through here; none may bypass it.
   *
   * The fault is recorded and logged, never swallowed — a silent catch is the
   * same failure one step removed.
   */
  private listen<E extends keyof RoomEventCallbacks>(
    event: E,
    handler: RoomEventCallbacks[E]
  ): void {
    const guarded = (...args: unknown[]): void => {
      try {
        (handler as (...callbackArgs: unknown[]) => void)(...args);
      } catch (error) {
        this.recordHandlerFault(event, error);
      }
    };
    this.room.on(event, guarded as RoomEventCallbacks[E]);
  }

  /**
   * Runs something that is not state propagation — a sound, a chime — off the
   * emit stack entirely, so it cannot unwind LiveKit's dispatch even if the
   * guard above is ever lost.
   */
  private deferSideEffect(event: string, effect: () => void): void {
    queueMicrotask(() => {
      try {
        effect();
      } catch (error) {
        this.recordHandlerFault(event, error);
      }
    });
  }

  private recordHandlerFault(event: string, error: unknown): void {
    this.handlerFaultLog.push({
      at: Date.now(),
      event,
      message: error instanceof Error ? (error.stack ?? error.message) : String(error)
    });
    if (this.handlerFaultLog.length > maximumHandlerFaults) this.handlerFaultLog.shift();
    console.error(`[tide] room handler for ${event} threw`, error);
  }

  private syncAllMediaState(): void {
    this.micEnabled = this.room.localParticipant.isMicrophoneEnabled;
    this.camEnabled = this.room.localParticipant.isCameraEnabled;
    this.screenShareEnabled = this.room.localParticipant.isScreenShareEnabled;
    this.markProjectionDirty();
  }

  private handleDisconnected(reason?: DisconnectReason): void {
    this.disconnectReason = reason;
    this.wasRemoved = reason === DisconnectReason.PARTICIPANT_REMOVED;
    this.connectionState = ConnectionState.Disconnected;
    this.participants = [];
    this.projectionSignature = '';
    this.projectionDirty = false;
    this.projectionAuthoritative = false;
    this.stopProjectionHeartbeat();
    this.hiddenCameraIdentities = [];
    this.activeSpeakerIdentities = [];
    this.micEnabled = false;
    this.camEnabled = false;
    this.screenShareEnabled = false;
    this.isRecording = false;
    this.hasSyncedRoomMetadata = false;
    this.canPlaybackAudio = true;
    this.canPlaybackVideo = true;
    this.mediaPlaybackEpoch += 1;
    this.mediaPlaybackAttempt += 1;
    this.mediaPlaybackAttemptInFlight = false;
    this.mediaPlaybackRecoveryAttempted = false;
    this.markPlaybackHealthy();
    this.subscriptionFailures = {};
    setConnectionChrome('offline');
  }

  private recordSubscriptionFailure(publicationSid: string, reason?: unknown): void {
    const publication = this.remotePublication(publicationSid);
    this.subscriptionFailures = {
      ...this.subscriptionFailures,
      [publicationSid]: {
        publicationSid,
        message:
          reason === undefined
            ? 'Could not subscribe to this media publication.'
            : `Could not subscribe to this media publication (LiveKit error ${String(reason)}).`,
        // Recoverable means the request still stands: the publication exists,
        // we still want it, and we are allowed to have it. Recovery is the
        // next reconcile tick re-deriving the subscription — tide does not
        // run its own retry loop against the SFU, because livekit-client
        // already re-establishes subscriptions from sendSyncState().
        recoverable:
          publication !== undefined &&
          publication.isDesired &&
          publication.permissionStatus === TrackPublication.PermissionStatus.Allowed,
        occurredAt: Date.now()
      }
    };
    this.markProjectionDirty();
  }

  private armMediaPlaybackUnlock(): void {
    if (typeof document === 'undefined' || this.mediaPlaybackUnlock) return;
    const controller = new AbortController();
    const activate = (): void => {
      controller.abort();
      this.mediaPlaybackUnlock = undefined;
      this.activateMediaPlayback();
    };
    document.addEventListener('pointerdown', activate, {
      capture: true,
      once: true,
      signal: controller.signal
    });
    document.addEventListener('keydown', activate, {
      capture: true,
      once: true,
      signal: controller.signal
    });
    this.mediaPlaybackUnlock = controller;
  }

  private clearMediaPlaybackUnlock(): void {
    this.mediaPlaybackUnlock?.abort();
    this.mediaPlaybackUnlock = undefined;
  }

  private remotePublication(publicationSid: string): RemoteTrackPublication | undefined {
    for (const participant of this.room.remoteParticipants.values()) {
      const publication = participant.trackPublications.get(publicationSid);
      if (publication) return publication as RemoteTrackPublication;
    }
    return undefined;
  }

  /**
   * States what tide wants subscribed and lets the difference drive the SFU.
   * Every publication of every remote participant is desired except a camera
   * whose owner the local user has hidden. Nothing is toggled that already
   * matches, so this is free on an unchanged room and self-repairing on a
   * room that drifted.
   */
  private applySubscriptions(): void {
    for (const [identity, participant] of this.room.remoteParticipants) {
      const cameraHidden = this.isParticipantCameraHidden(identity);
      for (const publication of participant.trackPublications.values()) {
        const remote = publication as RemoteTrackPublication;
        if (remote.permissionStatus === TrackPublication.PermissionStatus.NotAllowed) continue;
        const desired = !(cameraHidden && remote.source === Track.Source.Camera);
        if (remote.isDesired === desired) continue;
        if (desired) this.clearSubscriptionFailure(remote.trackSid);
        remote.setSubscribed(desired);
      }
    }
  }

  private pruneHiddenCameraIdentities(): void {
    const connected = new Set(this.room.remoteParticipants.keys());
    const remaining = this.hiddenCameraIdentities.filter((identity) => connected.has(identity));
    // Assign only on a real change: this runs on every heartbeat tick, and a
    // fresh array would invalidate every consumer of the list.
    if (remaining.length !== this.hiddenCameraIdentities.length) {
      this.hiddenCameraIdentities = remaining;
    }
  }

  private clearSubscriptionFailure(publicationSid: string): void {
    if (!(publicationSid in this.subscriptionFailures)) return;
    const next = { ...this.subscriptionFailures };
    delete next[publicationSid];
    this.subscriptionFailures = next;
  }

  /**
   * The failure set is re-derived like everything else: a failure that no
   * longer describes anything — the publication is gone, or it is subscribed
   * after all — is dropped on the next tick rather than waiting for an event
   * that may never come.
   */
  private pruneSubscriptionFailures(participants: Participant[]): void {
    const recorded = Object.keys(this.subscriptionFailures);
    if (recorded.length === 0) return;
    const live = new Map<string, TrackPublication>();
    for (const participant of participants) {
      for (const [publicationSid, publication] of participant.trackPublications) {
        live.set(publicationSid, publication);
      }
    }
    for (const publicationSid of recorded) {
      const publication = live.get(publicationSid);
      if (!publication || publication.isSubscribed) this.clearSubscriptionFailure(publicationSid);
    }
  }

  /**
   * A snapshot of what this client observed and what it did with it. Stays on
   * the machine that produced it: identities and display names are personal
   * data, so nothing is auto-shipped and the export is an explicit action.
   */
  diagnostics(): Record<string, unknown> {
    return {
      capturedAt: new Date().toISOString(),
      connectionState: this.connectionState,
      playback: {
        audio: this.canPlaybackAudio,
        video: this.canPlaybackVideo,
        error: this.mediaPlaybackError
      },
      projection: { ticks: this.projectionTicks, revisions: this.projectionRevisions },
      localIdentity: this.room.localParticipant.identity,
      participants: [...this.room.remoteParticipants.values()].map((participant) => ({
        identity: participant.identity,
        kind: participant.kind,
        publications: [...participant.trackPublications.values()].map((publication) => ({
          publicationSid: publication.trackSid,
          source: publication.source,
          kind: publication.kind,
          muted: publication.isMuted,
          subscribed: publication.isSubscribed,
          desired: (publication as RemoteTrackPublication).isDesired,
          subscriptionStatus: publication.subscriptionStatus
        }))
      })),
      subscriptionFailures: this.subscriptionFailures,
      handlerFaults: [...this.handlerFaultLog],
      events: [...this.eventLedger]
    };
  }

  downloadDiagnostics(): void {
    if (typeof document === 'undefined') return;
    const blob = new Blob([JSON.stringify(this.diagnostics(), null, 2)], {
      type: 'application/json'
    });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = `tide-diagnostics-${Date.now()}.json`;
    anchor.click();
    URL.revokeObjectURL(url);
  }

  async mediaProbeSnapshot(): Promise<{
    connectionState: ConnectionState;
    playback: { audio: boolean; video: boolean; error: string };
    projection: { ticks: number; revisions: number };
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
  }> {
    // The same filtered view reconcileMedia projects: a probe that read the
    // raw maps would report a ghost's publications as expected elements the
    // UI (correctly) no longer renders.
    const probeParticipants = projectParticipants(
      this.connectedParticipants(),
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
      projection: { ticks: this.projectionTicks, revisions: this.projectionRevisions },
      participants: probeParticipants.map((participant) => ({
        identity: participant.identity,
        name: participant.name,
        isLocal: participant.isLocal,
        isEgress:
          this.room.remoteParticipants.get(participant.identity)?.kind === ParticipantKind.EGRESS,
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
      subscriptionFailures: this.subscriptionFailures
    };
  }

  /**
   * Registered before tide's own handlers so the ledger records what LiveKit
   * emitted, not what a handler decided to do about it. Final state alone
   * cannot distinguish "never told" from "told and mishandled"; the sequence
   * can, and that is what an incident report is read from.
   */
  private installEventLedger(): void {
    const record = (event: string, entry: Omit<MediaLedgerEntry, 'at' | 'event'> = {}): void => {
      this.eventLedger.push({ at: Date.now(), event, ...entry });
      if (this.eventLedger.length > maximumLedgerEntries) this.eventLedger.shift();
    };
    this.listen(RoomEvent.ConnectionStateChanged, (state) =>
      record('ConnectionStateChanged', { detail: state })
    );
    this.listen(RoomEvent.ParticipantConnected, (participant) =>
      record('ParticipantConnected', { identity: participant.identity })
    );
    this.listen(RoomEvent.ParticipantDisconnected, (participant) =>
      record('ParticipantDisconnected', { identity: participant.identity })
    );
    this.listen(RoomEvent.TrackPublished, (publication, participant) =>
      record('TrackPublished', {
        identity: participant.identity,
        publicationSid: publication.trackSid,
        detail: publication.source
      })
    );
    this.listen(RoomEvent.TrackUnpublished, (publication, participant) =>
      record('TrackUnpublished', {
        identity: participant.identity,
        publicationSid: publication.trackSid
      })
    );
    this.listen(RoomEvent.TrackSubscribed, (_track, publication, participant) =>
      record('TrackSubscribed', {
        identity: participant.identity,
        publicationSid: publication.trackSid,
        detail: publication.source
      })
    );
    this.listen(RoomEvent.TrackUnsubscribed, (_track, publication, participant) =>
      record('TrackUnsubscribed', {
        identity: participant.identity,
        publicationSid: publication.trackSid
      })
    );
    this.listen(RoomEvent.TrackSubscriptionFailed, (publicationSid, participant, reason) =>
      record('TrackSubscriptionFailed', {
        identity: participant.identity,
        publicationSid,
        detail: String(reason)
      })
    );
    this.listen(RoomEvent.TrackMuted, (publication, participant) =>
      record('TrackMuted', {
        identity: participant.identity,
        publicationSid: publication.trackSid
      })
    );
    this.listen(RoomEvent.TrackUnmuted, (publication, participant) =>
      record('TrackUnmuted', {
        identity: participant.identity,
        publicationSid: publication.trackSid
      })
    );
    this.listen(RoomEvent.Reconnecting, () => record('Reconnecting'));
    this.listen(RoomEvent.SignalReconnecting, () => record('SignalReconnecting'));
    this.listen(RoomEvent.Reconnected, () => record('Reconnected'));
  }

  private installMediaTestHooks(): void {
    if (import.meta.env.VITE_TIDE_TEST !== 'true' || typeof window === 'undefined') return;
    type PlaybackKind = 'audio' | 'video' | 'any';
    type TestAPI = {
      attachmentDelayMs: number;
      snapshot: () => ReturnType<RoomState['mediaProbeSnapshot']>;
      reconcile: () => void;
      clearSrcObject: (publicationSid: string) => boolean;
      forgetLiveKitAttachment: (publicationSid: string) => boolean;
      injectSubscriptionFailure: (publicationSid: string) => void;
      unsubscribeBehindBack: (publicationSid: string) => boolean;
      rejectNextPlayback: (kind?: PlaybackKind) => void;
      blockPlayback: (kind?: PlaybackKind) => void;
      unblockPlayback: () => void;
      playCalls: () => number;
      ledger: () => MediaLedgerEntry[];
      clearLedger: () => void;
      uncaughtErrors: () => string[];
      handlerFaults: () => HandlerFault[];
      failNextParticipantEntered: () => void;
      failNextReconcile: () => void;
      deafen: () => void;
    };
    const testWindow = window as Window & { __tideMediaTest?: TestAPI };
    let rejectedKind: PlaybackKind | undefined;
    let blockedKind: PlaybackKind | undefined;
    const nativePlay = HTMLMediaElement.prototype.play;
    const blocked = (): Promise<void> =>
      Promise.reject(new DOMException('Playback blocked by Tide test hook.', 'NotAllowedError'));
    let playCalls = 0;
    HTMLMediaElement.prototype.play = function (): Promise<void> {
      playCalls += 1;
      const kind: PlaybackKind = this instanceof HTMLAudioElement ? 'audio' : 'video';
      if (blockedKind === 'any' || blockedKind === kind) return blocked();
      if (rejectedKind === 'any' || rejectedKind === kind) {
        rejectedKind = undefined;
        return blocked();
      }
      return nativePlay.call(this);
    };

    // A throw inside a RoomEvent listener escapes through LiveKit's signal
    // handling to the global handler. Every scenario asserts this stays empty,
    // so the failure is caught even where it is not deliberately injected.
    const uncaught: string[] = [];
    window.addEventListener('error', (event) => uncaught.push(String(event.message)));
    window.addEventListener('unhandledrejection', (event) =>
      uncaught.push(String((event as PromiseRejectionEvent).reason))
    );

    const announceParticipantEntered = this.announceParticipantEntered;
    const reconcileMedia = (authoritative: boolean): void => {
      RoomState.prototype.reconcileMedia.call(this, authoritative);
    };

    testWindow.__tideMediaTest = {
      attachmentDelayMs: 0,
      snapshot: () => this.mediaProbeSnapshot(),
      reconcile: () => this.reconcileMedia(true),
      ledger: () => [...this.eventLedger],
      clearLedger: () => {
        this.eventLedger.length = 0;
      },
      uncaughtErrors: () => [...uncaught],
      handlerFaults: () => [...this.handlerFaultLog],
      failNextParticipantEntered: () => {
        this.announceParticipantEntered = () => {
          this.announceParticipantEntered = announceParticipantEntered;
          throw new Error('Tide test hook: participant-entered fault.');
        };
      },
      failNextReconcile: () => {
        this.reconcileMedia = () => {
          this.reconcileMedia = reconcileMedia;
          throw new Error('Tide test hook: reconcile fault.');
        };
      },
      // Drops every application listener and reinstates only the ledger, so
      // the room keeps running while tide is told nothing. LiveKit never
      // listens to its own RoomEvents, so this silences the app alone. It
      // models the general case behind every media incident here: an event
      // that never arrives. A client that only reduces events stays frozen;
      // one that re-derives from LiveKit's own maps catches up regardless.
      deafen: () => {
        for (const event of Object.values(RoomEvent)) this.room.removeAllListeners(event);
        this.installEventLedger();
      },
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
      injectSubscriptionFailure: (publicationSid) => {
        this.recordSubscriptionFailure(publicationSid);
      },
      // Unsubscribes without telling tide's own state, the way a stray SDK
      // path or a racing UI toggle would. applySubscriptions must put it back.
      unsubscribeBehindBack: (publicationSid) => {
        const publication = this.remotePublication(publicationSid);
        if (!publication) return false;
        publication.setSubscribed(false);
        return true;
      },
      rejectNextPlayback: (kind = 'any') => {
        rejectedKind = kind;
      },
      blockPlayback: (kind = 'any') => {
        blockedKind = kind;
      },
      unblockPlayback: () => {
        blockedKind = undefined;
      },
      playCalls: () => playCalls
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
      // Ignore data messages that are not valid tide chat payloads.
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
