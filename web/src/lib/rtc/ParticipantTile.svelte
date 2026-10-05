<script lang="ts">
  import { MicrophoneSlash } from 'phosphor-svelte';
  import Waveform from '$lib/ui/Waveform.svelte';
  import { clampAspect, observeAspect } from './aspect';
  import type { ParticipantView } from './media';
  import { attachMediaTrack } from './mediaElement';
  import ParticipantMenu from './ParticipantMenu.svelte';

  let {
    participant,
    speaking = false,
    fit = 'contain',
    waveform = true,
    promoted = false,
    rail = false,
    railRow = 1,
    videoHidden = false,
    menuOpen = false,
    canManage = false,
    onmenuopenchange,
    ontogglevideo,
    onmute,
    onremove
  }: {
    participant: ParticipantView;
    speaking?: boolean;
    fit?: 'contain' | 'width';
    waveform?: boolean;
    promoted?: boolean;
    rail?: boolean;
    railRow?: number;
    videoHidden?: boolean;
    menuOpen?: boolean;
    canManage?: boolean;
    onmenuopenchange?: (open: boolean) => void;
    ontogglevideo?: () => void | Promise<void>;
    onmute?: () => void | Promise<void>;
    onremove?: () => void | Promise<void>;
  } = $props();

  let aspect = $state(clampAspect(0, 0));

  $effect(() => {
    // Camera gone (or not yet up): placeholder tiles rest at 16:9.
    if (!participant.camera) aspect = clampAspect(0, 0);
  });

  function initialFor(name: string): string {
    return name.slice(0, 1).toUpperCase() || '?';
  }
</script>

<article
  class="cell"
  class:fit-width={fit === 'width'}
  class:promoted
  class:rail
  style:--tile-row={railRow}
  data-testid="participant-tile"
  data-identity={participant.identity}
  data-video-hidden={videoHidden}
>
  <div class="video-box" class:speaking style:--va={aspect}>
    {#if participant.camera && !videoHidden}
      <!-- Live meeting video does not have a caption track. -->
      <!-- Always muted: audio plays through the persistent remote renderer,
           and an unmuted <video> can be blocked from autoplaying. -->
      <video
        use:attachMediaTrack={participant.camera}
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

    {#if videoHidden}<span class="video-hidden mono">Video hidden</span>{/if}

    {#if !participant.isLocal && ontogglevideo && onmenuopenchange}
      <ParticipantMenu
        {participant}
        open={menuOpen}
        {videoHidden}
        {canManage}
        onopenchange={onmenuopenchange}
        {ontogglevideo}
        {onmute}
        {onremove}
      />
    {/if}

    <div class="name-label">
      <span class="name">{participant.name}</span>
      {#if participant.isLocal}<span class="you">(You)</span>{/if}
      {#if participant.micMuted}
        <MicrophoneSlash size={16} weight="regular" aria-label="Microphone muted" />
      {/if}
    </div>

    {#if waveform}
      <Waveform
        track={participant.microphone && !participant.microphone.muted
          ? participant.microphone.track
          : undefined}
      />
    {/if}
  </div>
</article>

<style>
  .cell {
    position: relative;
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
    overflow: visible;
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

  .cell.promoted {
    grid-row: 1 / -1;
    grid-column: 1;
  }

  .cell.rail {
    grid-row: var(--tile-row);
    grid-column: 2;
  }

  video,
  .placeholder {
    grid-area: 1 / 1;
    width: 100%;
    height: 100%;
    border-radius: calc(var(--radius-tile) - 1px);
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

  .video-hidden {
    position: absolute;
    top: 8px;
    left: 8px;
    padding: 2px 6px;
    color: var(--text-2);
    font-size: 10px;
    line-height: 16px;
    background: color-mix(in srgb, var(--stage) 78%, transparent);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  .cell:focus-within {
    z-index: 5;
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
    flex: none;
    color: var(--text-2);
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
