import { ConnectionState, Room, RoomEvent, Track, type Participant } from 'livekit-client';

export interface ParticipantView {
  identity: string;
  name: string;
  isLocal: boolean;
  videoTracks: Track[];
  audioTracks: Track[];
}

export class RoomState {
  readonly room = new Room({ adaptiveStream: true, dynacast: true });

  connectionState = $state<ConnectionState>(ConnectionState.Disconnected);
  participants = $state<ParticipantView[]>([]);

  constructor() {
    this.room
      .on(RoomEvent.ConnectionStateChanged, (state: ConnectionState) => {
        this.connectionState = state;
      })
      .on(RoomEvent.ParticipantConnected, () => this.syncParticipants())
      .on(RoomEvent.ParticipantDisconnected, () => this.syncParticipants())
      .on(RoomEvent.TrackPublished, () => this.syncParticipants())
      .on(RoomEvent.TrackUnpublished, () => this.syncParticipants())
      .on(RoomEvent.TrackSubscribed, () => this.syncParticipants())
      .on(RoomEvent.TrackUnsubscribed, () => this.syncParticipants())
      .on(RoomEvent.LocalTrackPublished, () => this.syncParticipants())
      .on(RoomEvent.LocalTrackUnpublished, () => this.syncParticipants());
  }

  async connect(wsURL: string, token: string): Promise<void> {
    try {
      await this.room.connect(wsURL, token);
      this.syncParticipants();
      await this.room.localParticipant.enableCameraAndMicrophone();
      this.syncParticipants();
    } catch (error) {
      await this.disconnect();
      throw error;
    }
  }

  async disconnect(): Promise<void> {
    await this.room.disconnect();
    this.connectionState = ConnectionState.Disconnected;
    this.participants = [];
  }

  private syncParticipants(): void {
    const participants: Participant[] = [
      this.room.localParticipant,
      ...this.room.remoteParticipants.values()
    ];

    this.participants = participants.map((participant) => {
      const tracks = [...participant.trackPublications.values()].flatMap((publication) =>
        publication.track ? [publication.track] : []
      );
      return {
        identity: participant.identity,
        name: participant.name || participant.identity,
        isLocal: participant === this.room.localParticipant,
        videoTracks: tracks.filter((track) => track.kind === Track.Kind.Video),
        audioTracks: tracks.filter((track) => track.kind === Track.Kind.Audio)
      };
    });
  }
}
