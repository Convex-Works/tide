<script lang="ts">
  import type { Track } from 'livekit-client';
  import type { RoomState } from './room.svelte';

  let { state, roomName }: { state: RoomState; roomName: string } = $props();

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
</script>

<main class="stage">
  <header class="stage-header">
    <span class="slug mono">{roomName}</span>
    <span class="connection">{state.connectionState}</span>
  </header>

  <section class="grid" aria-label="Meeting participants">
    {#each state.participants as participant (participant.identity)}
      <article class="tile">
        {#if participant.videoTracks.length === 0}
          <div class="placeholder" aria-hidden="true">
            {participant.name.slice(0, 1).toUpperCase()}
          </div>
        {/if}

        {#each participant.videoTracks as track}
          <!-- Live meeting video does not have a caption track. -->
          <!-- svelte-ignore a11y_media_has_caption -->
          <video
            use:attachTrack={track}
            autoplay
            playsinline
            muted={participant.isLocal}
            class:mirrored={participant.isLocal}
            aria-label={`${participant.name}'s video`}
          ></video>
        {/each}

        {#each participant.audioTracks as track}
          <!-- Live meeting audio does not have a caption track. -->
          <!-- svelte-ignore a11y_media_has_caption -->
          <audio
            use:attachTrack={track}
            autoplay
            muted={participant.isLocal}
            aria-label={`${participant.name}'s audio`}
          ></audio>
        {/each}

        <div class="name">{participant.name}{participant.isLocal ? ' · You' : ''}</div>
      </article>
    {/each}
  </section>
</main>

<style>
  .stage {
    min-height: 100dvh;
    padding: 14px 12px 12px;
    color: var(--text);
    background: var(--stage);
  }

  .stage-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 28px;
    margin-bottom: 8px;
  }

  .slug {
    padding: 2px 7px;
    font-size: 11px;
    line-height: 18px;
    color: var(--text);
    border: 1px solid var(--border-d);
    border-radius: 999px;
  }

  .connection {
    color: var(--text-2);
    font-size: 12px;
    text-transform: lowercase;
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(320px, 100%), 1fr));
    gap: 8px;
  }

  .tile {
    position: relative;
    display: grid;
    min-height: 220px;
    overflow: hidden;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-tile);
    aspect-ratio: 16 / 9;
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

  .name {
    position: absolute;
    bottom: 8px;
    left: 8px;
    padding: 2px 6px;
    color: var(--text);
    font-size: 12px;
    background: rgb(15 15 14 / 78%);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }
</style>
