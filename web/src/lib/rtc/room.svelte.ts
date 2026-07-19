import {
  ConnectionState,
  DisconnectReason,
  Room,
  RoomEvent,
  Track,
  type Participant,
  type RemoteParticipant
} from 'livekit-client';
import { setConnectionChrome } from './connection.svelte';

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

export interface ParticipantView {
  identity: string;
  name: string;
  isLocal: boolean;
  role?: 'host';
  cameraTrack?: Track;
  screenShareTrack?: Track;
  audioTracks: Track[];
  micMuted: boolean;
  camMuted: boolean;
  isSpeaking: boolean;
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
  devices = $state<DeviceLists>({ audioinput: [], videoinput: [], audiooutput: [] });
  activeDeviceIds = $state<ActiveDeviceIds>({
    audioinput: '',
    videoinput: '',
    audiooutput: ''
  });

  constructor() {
    this.room
      .on(RoomEvent.ConnectionStateChanged, (state: ConnectionState) => {
        this.connectionState = state;
        this.syncConnectionChrome(state);
      })
      .on(RoomEvent.Disconnected, (reason?: DisconnectReason) => this.handleDisconnected(reason))
      .on(RoomEvent.ParticipantConnected, () => this.syncParticipants())
      .on(RoomEvent.ParticipantDisconnected, () => this.syncParticipants())
      .on(RoomEvent.TrackPublished, () => this.syncParticipants())
      .on(RoomEvent.TrackUnpublished, () => this.syncParticipants())
      .on(RoomEvent.TrackSubscribed, () => this.syncParticipants())
      .on(RoomEvent.TrackUnsubscribed, () => this.syncParticipants())
      .on(RoomEvent.TrackMuted, () => this.syncAllMediaState())
      .on(RoomEvent.TrackUnmuted, () => this.syncAllMediaState())
      .on(RoomEvent.LocalTrackPublished, () => this.syncAllMediaState())
      .on(RoomEvent.LocalTrackUnpublished, () => this.syncAllMediaState())
      .on(RoomEvent.ParticipantMetadataChanged, () => this.syncParticipants())
      .on(RoomEvent.ParticipantNameChanged, () => this.syncParticipants())
      .on(RoomEvent.RoomMetadataChanged, () => this.syncRoomMetadata())
      .on(
        RoomEvent.DataReceived,
        (payload: Uint8Array, participant?: RemoteParticipant, _kind?: unknown, topic?: string) => {
          if (topic === 'chat') this.receiveChat(payload, participant);
        }
      )
      .on(RoomEvent.ActiveSpeakersChanged, (speakers: Participant[]) => {
        this.activeSpeakerIdentities = speakers.map((speaker) => speaker.identity);
        this.syncParticipants();
      })
      .on(RoomEvent.MediaDevicesChanged, () => {
        void this.refreshDevices().catch(() => undefined);
      });

    if (import.meta.env.DEV && typeof window !== 'undefined') {
      (window as Window & { __klisiRoom?: Room }).__klisiRoom = this.room;
    }
  }

  async connect(wsURL: string, token: string, media: JoinMediaOptions): Promise<void> {
    this.chat = [];
    this.chatRevision = 0;
    this.disconnectReason = undefined;
    this.wasRemoved = false;
    try {
      await this.room.connect(wsURL, token);
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

  private syncParticipants(): void {
    const participants: Participant[] = [
      this.room.localParticipant,
      ...this.room.remoteParticipants.values()
    ];

    this.participants = participants.map((participant) => {
      const publications = [...participant.trackPublications.values()];
      const camera = publications.find(
        (publication) => publication.source === Track.Source.Camera && !publication.isMuted
      );
      const screenShare = publications.find(
        (publication) => publication.source === Track.Source.ScreenShare && !publication.isMuted
      );
      const microphone = publications.find(
        (publication) => publication.source === Track.Source.Microphone
      );

      return {
        identity: participant.identity,
        name: participant.name || participant.identity,
        isLocal: participant === this.room.localParticipant,
        role: this.participantRole(participant),
        cameraTrack: camera?.track,
        screenShareTrack: screenShare?.track,
        audioTracks: publications.flatMap((publication) => {
          const track = publication.track;
          return publication.kind === Track.Kind.Audio && track && !publication.isMuted
            ? [track]
            : [];
        }),
        micMuted: !microphone || microphone.isMuted,
        camMuted: !camera || camera.isMuted,
        isSpeaking: this.activeSpeakerIdentities.includes(participant.identity)
      };
    });
  }

  private syncAllMediaState(): void {
    this.micEnabled = this.room.localParticipant.isMicrophoneEnabled;
    this.camEnabled = this.room.localParticipant.isCameraEnabled;
    this.screenShareEnabled = this.room.localParticipant.isScreenShareEnabled;
    this.syncParticipants();
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
    setConnectionChrome('offline');
  }

  private participantRole(participant: Participant): 'host' | undefined {
    if (!participant.metadata) return undefined;
    try {
      const metadata = JSON.parse(participant.metadata) as { role?: unknown };
      return metadata.role === 'host' ? 'host' : undefined;
    } catch {
      return undefined;
    }
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
    this.isRecording = recording;
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
