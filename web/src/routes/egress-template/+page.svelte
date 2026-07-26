<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { Room, RoomEvent } from 'livekit-client';
  import ParticipantTile from '$lib/rtc/ParticipantTile.svelte';
  import RemoteAudioRenderer from '$lib/rtc/RemoteAudioRenderer.svelte';
  import { projectParticipants, type ParticipantView } from '$lib/rtc/media';
  import { attachMediaTrack } from '$lib/rtc/mediaElement';

  const rtc = new Room({ adaptiveStream: false, dynacast: false });
  let participants = $state<ParticipantView[]>([]);
  let layout = $state('grid');
  let error = $state('');
  let connected = false;
  let trackSubscribed = false;
  let recordingStarted = false;
  let recordingEnded = false;
  let mediaFatal = false;
  let readinessTimer: ReturnType<typeof setTimeout> | undefined;
  let fallbackTimer: ReturnType<typeof setTimeout> | undefined;

  let focusParticipant = $derived(participants.find((participant) => participant.screenShare));
  let gridColumns = $derived(Math.min(3, Math.max(1, Math.ceil(Math.sqrt(participants.length)))));
  let gridRows = $derived(Math.max(1, Math.ceil(participants.length / gridColumns)));

  function syncParticipants(): void {
    participants = projectParticipants([...rtc.remoteParticipants.values()]);
  }

  function fatalMedia(reason: string, details: Record<string, unknown> = {}): void {
    mediaFatal = true;
    error = 'Recording media could not start.';
    console.error(
      `KLISI_MEDIA_FATAL ${JSON.stringify({
        event: 'egress_media_fatal',
        reason,
        playback: {
          audio: rtc.canPlaybackAudio,
          video: rtc.canPlaybackVideo
        },
        ...details
      })}`
    );
  }

  function maybeStartRecording(): void {
    if (!connected || !trackSubscribed || recordingStarted || mediaFatal) return;
    if (readinessTimer) clearTimeout(readinessTimer);
    readinessTimer = setTimeout(() => {
      if (recordingStarted || mediaFatal || !rtc.canPlaybackAudio || !rtc.canPlaybackVideo) {
        if (!mediaFatal) fatalMedia('playback_not_ready');
        return;
      }
      recordingStarted = true;
      if (fallbackTimer) clearTimeout(fallbackTimer);
      console.log('START_RECORDING');
    }, 250);
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
      .on(RoomEvent.TrackStreamStateChanged, syncParticipants)
      .on(RoomEvent.TrackSubscriptionStatusChanged, syncParticipants)
      .on(RoomEvent.TrackSubscriptionPermissionChanged, syncParticipants)
      .on(RoomEvent.TrackSubscribed, () => {
        trackSubscribed = true;
        syncParticipants();
        maybeStartRecording();
      })
      .on(RoomEvent.TrackUnsubscribed, syncParticipants)
      .on(RoomEvent.TrackSubscriptionFailed, (publicationSid, participant, reason) => {
        fatalMedia('subscription_failed', {
          publicationSid,
          participantIdentity: participant.identity,
          subscriptionError: reason
        });
      })
      .on(RoomEvent.Reconnected, () => {
        syncParticipants();
        maybeStartRecording();
      })
      .on(RoomEvent.AudioPlaybackStatusChanged, (playing) => {
        if (!playing) fatalMedia('audio_playback_blocked');
      })
      .on(RoomEvent.VideoPlaybackStatusChanged, (playing) => {
        if (!playing) fatalMedia('video_playback_blocked');
      })
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
    if (readinessTimer) clearTimeout(readinessTimer);
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
  {:else}
    <section
      class="media-layout"
      class:screen-mode={Boolean(focusParticipant?.screenShare)}
      class:grid-mode={!focusParticipant?.screenShare}
      aria-label="Recording participants"
    >
      {#if focusParticipant?.screenShare}
        <article class="focus-pane">
          {#key focusParticipant.screenShare.publicationSid}
            <!-- The recording composite captures live media without captions. -->
            <video
              use:attachMediaTrack={focusParticipant.screenShare}
              autoplay
              playsinline
              muted
              aria-label={`${focusParticipant.name}'s screen share`}
            ></video>
          {/key}
          <div class="label">
            <span>{focusParticipant.name}</span>
            <span class="mono secondary">Screen</span>
          </div>
        </article>
      {/if}
      <div
        class="participant-list"
        data-count={participants.length}
        style={`--grid-columns: ${gridColumns}; --grid-rows: ${gridRows}`}
      >
        {#each participants as participant (participant.identity)}
          <!-- No waveforms in the recording: the audio itself is the artifact,
               and animated bars burn egress-worker CPU for no information. -->
          <ParticipantTile
            {participant}
            fit={focusParticipant?.screenShare ? 'width' : 'contain'}
            waveform={false}
          />
        {/each}
      </div>
    </section>
    <RemoteAudioRenderer {participants} />
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

  .media-layout {
    width: 100%;
    height: 100%;
    min-height: 0;
  }

  .participant-list {
    min-width: 0;
    min-height: 0;
  }

  .grid-mode .participant-list {
    display: grid;
    width: 100%;
    height: 100%;
    grid-template-columns: repeat(var(--grid-columns), minmax(0, 1fr));
    grid-template-rows: repeat(var(--grid-rows), minmax(0, 1fr));
    gap: 8px;
  }

  .grid-mode .participant-list[data-count='1'] {
    width: min(100%, 1280px);
    margin-inline: auto;
  }

  .screen-mode {
    display: grid;
    grid-template-areas: 'focus rail';
    grid-template-columns: minmax(0, 1fr) 220px;
    gap: 8px;
  }

  .screen-mode .participant-list {
    display: grid;
    min-height: 0;
    grid-area: rail;
    grid-auto-rows: max-content;
    gap: 8px;
    overflow: hidden;
  }

  .focus-pane {
    position: relative;
    display: grid;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-tile);
    grid-area: focus;
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

  .focus-pane {
    background: var(--stage);
  }

  .focus-pane video {
    width: 100%;
    height: 100%;
    object-fit: contain;
  }

  .error {
    display: grid;
    height: 100%;
    place-items: center;
    color: var(--rec);
    font-size: 15px;
  }
</style>
