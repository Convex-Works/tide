<script lang="ts">
  import type { Track } from 'livekit-client';
  import { MicrophoneSlash } from 'phosphor-svelte';
  import Waveform from '$lib/ui/Waveform.svelte';
  import { clampAspect, observeAspect } from './aspect';

  interface TileParticipant {
    identity: string;
    name: string;
    isLocal?: boolean;
    cameraTrack?: Track;
    audioTracks: Track[];
    micTrack?: Track;
    micMuted?: boolean;
  }

  let {
    participant,
    speaking = false,
    fit = 'contain',
    waveform = true
  }: {
    participant: TileParticipant;
    speaking?: boolean;
    fit?: 'contain' | 'width';
    waveform?: boolean;
  } = $props();

  let aspect = $state(clampAspect(0, 0));

  $effect(() => {
    // Camera gone (or not yet up): placeholder tiles rest at 16:9.
    if (!participant.cameraTrack) aspect = clampAspect(0, 0);
  });

  function attachTrack(node: HTMLMediaElement, track: Track) {
    let attached = track;
    attached.attach(node);

    return {
      update(next: Track) {
        // Svelte re-fires action updates whenever the participants array is
        // rebuilt; re-attaching the already-attached track resets the media
        // element (black frame, audio dropout), so only swap real changes.
        if (next === attached) return;
        attached.detach(node);
        attached = next;
        attached.attach(node);
      },
      destroy() {
        attached.detach(node);
      }
    };
  }

  function initialFor(name: string): string {
    return name.slice(0, 1).toUpperCase() || '?';
  }
</script>

<article
  class="cell"
  class:fit-width={fit === 'width'}
  data-testid="participant-tile"
  data-identity={participant.identity}
>
  <div class="video-box" class:speaking style:--va={aspect}>
    {#if participant.cameraTrack}
      <!-- Live meeting video does not have a caption track. -->
      <!-- svelte-ignore a11y_media_has_caption -->
      <!-- Always muted: audio plays through the per-track <audio> elements,
           and an unmuted <video> can be blocked from autoplaying. -->
      <video
        use:attachTrack={participant.cameraTrack}
        use:observeAspect={(next) => (aspect = next)}
        autoplay
        playsinline
        muted
        class:mirrored={participant.isLocal}
        aria-label={`${participant.name}'s video`}
      ></video>
    {:else}
      <div class="placeholder" aria-hidden="true">{initialFor(participant.name)}</div>
    {/if}

    {#if !participant.isLocal}
      {#each participant.audioTracks as track}
        <!-- Live meeting audio does not have a caption track. -->
        <!-- svelte-ignore a11y_media_has_caption -->
        <audio use:attachTrack={track} autoplay aria-label={`${participant.name}'s audio`}></audio>
      {/each}
    {/if}

    <div class="name-label">
      <span>{participant.name}</span>
      {#if participant.isLocal}<span class="you mono">You</span>{/if}
      {#if participant.micMuted}
        <MicrophoneSlash size={16} weight="regular" aria-label="Microphone muted" />
      {/if}
    </div>

    {#if waveform}
      <Waveform track={participant.micTrack} />
    {/if}
  </div>
</article>

<style>
  .cell {
    display: grid;
    min-width: 0;
    min-height: 0;
    place-items: center;
    /* Size containment gives the video box exact cqh units to contain-fit
       its clamped aspect inside a uniform grid cell. */
    container-type: size;
  }

  .video-box {
    position: relative;
    display: grid;
    overflow: hidden;
    aspect-ratio: var(--va);
    /* Contain-fit at the clamped aspect: height-limited width, capped by the
       cell, floored at 140px so portrait tiles never become slivers. When a
       cap or the floor binds, object-fit: cover takes the minimal crop. */
    width: clamp(min(140px, 100%), calc(100cqh * var(--va)), 100%);
    max-height: 100%;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-tile);
    transition:
      border-color var(--motion-fast),
      width var(--motion-fast),
      aspect-ratio var(--motion-fast);
  }

  .video-box.speaking {
    border-color: var(--accent-d);
  }

  .cell.fit-width {
    container-type: normal;
  }

  .cell.fit-width .video-box {
    width: 100%;
    max-height: 65dvh;
  }

  video,
  .placeholder {
    grid-area: 1 / 1;
    width: 100%;
    height: 100%;
  }

  video {
    object-fit: cover;
  }

  video.mirrored {
    transform: scaleX(-1);
  }

  .placeholder {
    display: grid;
    place-items: center;
    color: var(--text-2);
    font-size: 18px;
  }

  .name-label {
    position: absolute;
    bottom: 8px;
    left: 8px;
    display: flex;
    align-items: center;
    gap: 5px;
    padding: 2px 6px;
    color: var(--text-2);
    font-size: 12px;
    background: color-mix(in srgb, var(--stage) 78%, transparent);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
    transition: color var(--motion-fast);
  }

  .speaking .name-label {
    color: var(--text);
  }

  .you {
    color: var(--text-2);
    font-size: 10px;
    text-transform: uppercase;
  }

  @media (max-width: 720px) {
    /* Narrow screens: width-driven sizing (auto grid rows have no definite
       height for size containment). */
    .cell {
      container-type: normal;
    }

    .video-box {
      width: 100%;
      max-height: 65dvh;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .video-box {
      transition: border-color var(--motion-fast);
    }
  }
</style>
