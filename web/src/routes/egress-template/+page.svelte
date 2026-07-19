<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import {
    Room,
    RoomEvent,
    Track,
    type RemoteParticipant
  } from 'livekit-client';

  interface CompositeParticipant {
    identity: string;
    name: string;
    cameraTrack?: Track;
    screenShareTrack?: Track;
    audioTracks: Track[];
  }

  const rtc = new Room({ adaptiveStream: false, dynacast: false });
  let participants = $state<CompositeParticipant[]>([]);
  let layout = $state('grid');
  let error = $state('');
  let connected = false;
  let trackSubscribed = false;
  let recordingStarted = false;
  let recordingEnded = false;
  let fallbackTimer: ReturnType<typeof setTimeout> | undefined;

  let focusParticipant = $derived(
    participants.find((participant) => participant.screenShareTrack)
  );
  let gridColumns = $derived(
    Math.min(3, Math.max(1, Math.ceil(Math.sqrt(participants.length))))
  );
  let gridRows = $derived(Math.max(1, Math.ceil(participants.length / gridColumns)));

  function syncParticipants(): void {
    participants = [...rtc.remoteParticipants.values()].map(participantView);
  }

  function participantView(participant: RemoteParticipant): CompositeParticipant {
    const publications = [...participant.trackPublications.values()];
    const camera = publications.find(
      (publication) => publication.source === Track.Source.Camera && !publication.isMuted
    );
    const screenShare = publications.find(
      (publication) => publication.source === Track.Source.ScreenShare && !publication.isMuted
    );
    return {
      identity: participant.identity,
      name: participant.name || participant.identity,
      cameraTrack: camera?.track,
      screenShareTrack: screenShare?.track,
      audioTracks: publications.flatMap((publication) => {
        const track = publication.track;
        return publication.kind === Track.Kind.Audio && track && !publication.isMuted
          ? [track]
          : [];
      })
    };
  }

  function attachTrack(node: HTMLMediaElement, track: Track) {
    let attached = track;
    attached.attach(node);
    return {
      update(next: Track) {
        attached.detach(node);
        attached = next;
        attached.attach(node);
      },
      destroy() {
        attached.detach(node);
      }
    };
  }

  function maybeStartRecording(): void {
    if (!connected || !trackSubscribed || recordingStarted) return;
    recordingStarted = true;
    if (fallbackTimer) clearTimeout(fallbackTimer);
    console.log('START_RECORDING');
  }

  function startFallback(): void {
    fallbackTimer = setTimeout(() => {
      trackSubscribed = true;
      maybeStartRecording();
    }, 3_000);
  }

  function endRecording(): void {
    if (recordingEnded) return;
    recordingEnded = true;
    console.log('END_RECORDING');
  }

  function initial(name: string): string {
    return name.slice(0, 1).toUpperCase() || '?';
  }

  onMount(() => {
    const query = new URLSearchParams(window.location.search);
    const url = query.get('url') ?? '';
    const token = query.get('token') ?? '';
    layout = query.get('layout') ?? 'grid';

    rtc
      .on(RoomEvent.ParticipantConnected, syncParticipants)
      .on(RoomEvent.ParticipantDisconnected, syncParticipants)
      .on(RoomEvent.TrackPublished, syncParticipants)
      .on(RoomEvent.TrackUnpublished, syncParticipants)
      .on(RoomEvent.TrackMuted, syncParticipants)
      .on(RoomEvent.TrackUnmuted, syncParticipants)
      .on(RoomEvent.TrackSubscribed, () => {
        trackSubscribed = true;
        syncParticipants();
        maybeStartRecording();
      })
      .on(RoomEvent.TrackUnsubscribed, syncParticipants)
      .on(RoomEvent.Disconnected, endRecording);

    if (!url || !token) {
      error = 'Missing LiveKit egress parameters.';
      return;
    }

    void rtc
      .connect(url, token, { autoSubscribe: true })
      .then(() => {
        connected = true;
        syncParticipants();
        maybeStartRecording();
        if (!recordingStarted) startFallback();
      })
      .catch(() => {
        error = 'Could not connect the recording layout.';
      });
  });

  onDestroy(() => {
    if (fallbackTimer) clearTimeout(fallbackTimer);
    void rtc.disconnect();
  });
</script>

<svelte:head>
  <title>klisi recording</title>
  <meta name="robots" content="noindex" />
</svelte:head>

<main class="composite" data-layout={layout}>
  {#if error}
    <div class="error">{error}</div>
  {:else if focusParticipant && focusParticipant.screenShareTrack}
    <section class="focus-layout" aria-label="Recording participants">
      <article class="focus-pane">
        <!-- The recording composite captures live media without captions. -->
        <!-- svelte-ignore a11y_media_has_caption -->
        <video
          use:attachTrack={focusParticipant.screenShareTrack}
          autoplay
          playsinline
          aria-label={`${focusParticipant.name}'s screen share`}
        ></video>
        <div class="label">
          <span>{focusParticipant.name}</span>
          <span class="mono secondary">Screen</span>
        </div>
      </article>
      <div class="camera-rail">
        {#each participants as participant (participant.identity)}
          <article class="tile">
            {#if participant.cameraTrack}
              <!-- svelte-ignore a11y_media_has_caption -->
              <video
                use:attachTrack={participant.cameraTrack}
                autoplay
                playsinline
                aria-label={`${participant.name}'s video`}
              ></video>
            {:else}
              <div class="placeholder">{initial(participant.name)}</div>
            {/if}
            {#each participant.audioTracks as track}
              <!-- svelte-ignore a11y_media_has_caption -->
              <audio use:attachTrack={track} autoplay aria-label={`${participant.name}'s audio`}></audio>
            {/each}
            <div class="label">{participant.name}</div>
          </article>
        {/each}
      </div>
    </section>
  {:else}
    <section
      class="grid"
      data-count={participants.length}
      style={`--grid-columns: ${gridColumns}; --grid-rows: ${gridRows}`}
      aria-label="Recording participants"
    >
      {#each participants as participant (participant.identity)}
        <article class="tile">
          {#if participant.cameraTrack}
            <!-- svelte-ignore a11y_media_has_caption -->
            <video
              use:attachTrack={participant.cameraTrack}
              autoplay
              playsinline
              aria-label={`${participant.name}'s video`}
            ></video>
          {:else}
            <div class="placeholder">{initial(participant.name)}</div>
          {/if}
          {#each participant.audioTracks as track}
            <!-- svelte-ignore a11y_media_has_caption -->
            <audio use:attachTrack={track} autoplay aria-label={`${participant.name}'s audio`}></audio>
          {/each}
          <div class="label">{participant.name}</div>
        </article>
      {/each}
    </section>
  {/if}
</main>

<style>
  :global(html),
  :global(body) {
    overflow: hidden;
    background: var(--stage);
  }

  .composite {
    width: 100vw;
    height: 100vh;
    padding: 12px;
    color: var(--text);
    background: var(--stage);
  }

  .grid {
    display: grid;
    width: 100%;
    height: 100%;
    grid-template-columns: repeat(var(--grid-columns), minmax(0, 1fr));
    grid-template-rows: repeat(var(--grid-rows), minmax(0, 1fr));
    gap: 8px;
  }

  .grid[data-count='1'] {
    width: min(100%, 1280px);
    margin-inline: auto;
  }

  .tile,
  .focus-pane {
    position: relative;
    display: grid;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-tile);
  }

  .tile video,
  .placeholder {
    grid-area: 1 / 1;
    width: 100%;
    height: 100%;
  }

  .tile video {
    object-fit: cover;
  }

  .placeholder {
    display: grid;
    place-items: center;
    color: var(--text-2);
    font-size: 24px;
  }

  .label {
    position: absolute;
    bottom: 8px;
    left: 8px;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 2px 6px;
    color: var(--text);
    font-size: 12px;
    background: color-mix(in srgb, var(--stage) 78%, transparent);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  .secondary {
    color: var(--text-2);
    font-size: 10px;
    text-transform: uppercase;
  }

  .focus-layout {
    display: grid;
    width: 100%;
    height: 100%;
    grid-template-columns: minmax(0, 1fr) 220px;
    gap: 8px;
  }

  .focus-pane {
    background: var(--stage);
  }

  .focus-pane video {
    width: 100%;
    height: 100%;
    object-fit: contain;
  }

  .camera-rail {
    display: grid;
    min-height: 0;
    grid-auto-rows: minmax(120px, 1fr);
    gap: 8px;
    overflow: hidden;
  }

  .error {
    display: grid;
    height: 100%;
    place-items: center;
    color: var(--rec);
    font-size: 15px;
  }
</style>
