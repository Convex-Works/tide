import { ConnectionState, Room, RoomEvent, Track, type Participant } from 'livekit-client';
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
  cameraTrack?: Track;
  screenShareTrack?: Track;
  audioTracks: Track[];
  micMuted: boolean;
  isSpeaking: boolean;
}

export class RoomState {
  readonly room = new Room({ adaptiveStream: true, dynacast: true });

  connectionState = $state<ConnectionState>(ConnectionState.Disconnected);
  participants = $state<ParticipantView[]>([]);
  micEnabled = $state(false);
  camEnabled = $state(false);
  screenShareEnabled = $state(false);
  activeSpeakerIdentities = $state<string[]>([]);
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
      .on(RoomEvent.Disconnected, () => this.handleDisconnected())
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
    try {
      await this.room.connect(wsURL, token);

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
    this.handleDisconnected();
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
        cameraTrack: camera?.track,
        screenShareTrack: screenShare?.track,
        audioTracks: publications.flatMap((publication) => {
          const track = publication.track;
          return publication.kind === Track.Kind.Audio && track && !publication.isMuted
            ? [track]
            : [];
        }),
        micMuted: !microphone || microphone.isMuted,
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

  private handleDisconnected(): void {
    this.connectionState = ConnectionState.Disconnected;
    this.participants = [];
    this.activeSpeakerIdentities = [];
    this.micEnabled = false;
    this.camEnabled = false;
    this.screenShareEnabled = false;
    setConnectionChrome('offline');
  }

  private syncConnectionChrome(state: ConnectionState): void {
    if (
      state === ConnectionState.Reconnecting ||
      state === ConnectionState.SignalReconnecting
    ) {
      setConnectionChrome('reconnecting');
    } else if (state === ConnectionState.Disconnected) {
      setConnectionChrome('offline');
    } else {
      setConnectionChrome('connected');
    }
  }
}
