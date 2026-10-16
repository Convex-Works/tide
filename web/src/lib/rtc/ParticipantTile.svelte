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
    pip = false,
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
    fit?: 'contain' | 'width' | 'rail';
    waveform?: boolean;
    promoted?: boolean;
    rail?: boolean;
    /** Floats over the promoted tile's corner, as Meet shows your self-view in a call of two. */
    pip?: boolean;
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
  class:fit-rail={fit === 'rail'}
  class:promoted
  class:rail
  class:pip
  style:--tile-row={railRow}
  style:--va={aspect}
  data-testid="participant-tile"
  data-identity={participant.identity}
  data-video-hidden={videoHidden}
>
  <div class="video-box" class:speaking class:landscape={aspect >= 1}>
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

    <!-- The label gives way to the waveform: a long name ellipsizes on one
         line, and "(You)" and the muted icon always stay. -->
    <div class="tile-footer">
      <div class="name-label" title={participant.name}>
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
    /* One definite track, so the video's 100% sizes come from the box and a
       stream's own resolution can never stretch it. */
    grid-template: minmax(0, 1fr) / minmax(0, 1fr);
    overflow: visible;
    aspect-ratio: var(--va);
    /* Contain-fit at the clamped aspect: height-limited width, capped by the
       cell, floored at 140px so portrait tiles never become slivers. When a
       cap or the floor binds, object-fit: cover takes the minimal crop.
       Container units, not percentages: Safari let the percentage caps
       lose to the stream's size and overflowed the page sideways. */
    width: clamp(min(140px, 100cqw), calc(100cqh * var(--va)), 100cqw);
    max-height: 100cqh;
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

  /* Rail thumbnails are all one size, 16:9, as in Meet's sidebar, so a
     portrait camera doesn't take three times the room. A landscape stream
     covers its tile; a portrait one is shown whole between bars. */
  .cell.fit-rail {
    /* A crowded rail shares out less height than 16:9 needs; the tile then
       flattens to its row rather than overlap the next one. */
    grid-template: minmax(0, 1fr) / minmax(0, 1fr);
    container-type: normal;
  }

  .cell.fit-rail .video-box {
    width: 100%;
    max-height: 100%;
    aspect-ratio: 16 / 9;
  }

  .cell.fit-rail .video-box:not(.landscape) video {
    object-fit: contain;
  }

  /* The promoted tile fills its pane. When the stream and the pane share an
     orientation the video covers it, Meet-style; a portrait stream in a
     landscape pane (or the reverse) is shown whole instead of cropped to a
     strip. */
  .cell.promoted {
    grid-row: 1 / -1;
    grid-column: 1;
  }

  .cell.promoted .video-box {
    width: 100cqw;
    height: 100cqh;
    max-height: none;
    aspect-ratio: auto;
  }

  .cell.promoted video {
    object-fit: contain;
  }

  @container (orientation: landscape) {
    .cell.promoted .video-box.landscape video {
      object-fit: cover;
    }
  }

  @container (orientation: portrait) {
    .cell.promoted .video-box:not(.landscape) video {
      object-fit: cover;
    }
  }

  /* In a call of two, your self-view floats in the promoted pane's
     bottom-right corner: it shares the pane's grid area and sits on top.
     Landscape keeps the full
     width; anything narrower fits a box three quarters as tall, so a phone
     held upright doesn't cover the speaker, but never thinner than 96px. */
  .cell.pip {
    z-index: 2;
    grid-row: 1 / -1;
    grid-column: 1;
    align-self: end;
    justify-self: end;
    width: max(96px, calc(clamp(120px, 20%, 248px) * min(1, 0.75 * var(--va))));
    margin: 12px;
    container-type: normal;
  }

  .cell.pip .video-box {
    width: 100%;
    max-height: none;
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

  .tile-footer {
    position: absolute;
    right: 8px;
    bottom: 8px;
    left: 8px;
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 8px;
    pointer-events: none;
  }

  /* A pip may cover the promoted tile's bottom-right corner, so its waveform
     follows the name instead. */
  .promoted .tile-footer {
    justify-content: flex-start;
  }

  .name-label {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: 5px;
    padding: 2px 6px;
    color: var(--text-2);
    font-size: 12px;
    background: color-mix(in srgb, var(--stage) 78%, transparent);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
    white-space: nowrap;
    pointer-events: auto;
    transition: color var(--motion-fast);
  }

  .name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .name-label :global(svg) {
    flex: none;
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

    /* Speaker view on a phone: the promoted tile takes the screen, the
       others scroll below it. */
    .cell.promoted {
      height: calc(100dvh - 128px);
      container-type: size;
    }

    .cell.promoted,
    .cell.pip {
      grid-row: 1;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .video-box {
      transition: border-color var(--motion-fast);
    }
  }
</style>
