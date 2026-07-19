<script lang="ts">
  import type { Track } from 'livekit-client';
  import { MicrophoneSlash } from 'phosphor-svelte';
  import type { LobbyRequestInfo } from '$lib/api/types.gen';
  import ControlBar from '$lib/ui/ControlBar.svelte';
  import type { ParticipantView, RoomState } from './room.svelte';

  let {
    state,
    roomName,
    onleave,
    hostLobby = false,
    pending = [],
    lobbyOpen = $bindable(false),
    lobbyError = '',
    onadmit = () => undefined,
    ondeny = () => undefined
  }: {
    state: RoomState;
    roomName: string;
    onleave: () => void;
    hostLobby?: boolean;
    pending?: LobbyRequestInfo[];
    lobbyOpen?: boolean;
    lobbyError?: string;
    onadmit?: (id: string) => void | Promise<void>;
    ondeny?: (id: string) => void | Promise<void>;
  } = $props();

  let focusParticipant = $derived(
    state.participants.find((participant) => participant.screenShareTrack)
  );
  let gridColumns = $derived(
    Math.min(3, Math.max(1, Math.ceil(Math.sqrt(state.participants.length))))
  );
  let gridRows = $derived(Math.max(1, Math.ceil(state.participants.length / gridColumns)));
  let previousPendingCount = 0;

  $effect(() => {
    if (pending.length > 0 && previousPendingCount === 0) lobbyOpen = true;
    previousPendingCount = pending.length;
  });

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

  function initialFor(participant: ParticipantView): string {
    return participant.name.slice(0, 1).toUpperCase() || '?';
  }
</script>

{#snippet participantTile(participant: ParticipantView)}
  <article
    class:speaking={participant.isSpeaking}
    class="tile"
    data-testid="participant-tile"
    data-identity={participant.identity}
  >
    {#if participant.cameraTrack}
      <!-- Live meeting video does not have a caption track. -->
      <!-- svelte-ignore a11y_media_has_caption -->
      <video
        use:attachTrack={participant.cameraTrack}
        autoplay
        playsinline
        muted={participant.isLocal}
        class:mirrored={participant.isLocal}
        aria-label={`${participant.name}'s video`}
      ></video>
    {:else}
      <div class="placeholder" aria-hidden="true">{initialFor(participant)}</div>
    {/if}

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

    <div class="name-label">
      <span>{participant.name}</span>
      {#if participant.isLocal}<span class="you mono">You</span>{/if}
      {#if participant.micMuted}
        <MicrophoneSlash size={13} weight="regular" aria-label="Microphone muted" />
      {/if}
    </div>
  </article>
{/snippet}

<main class="stage">
  <header class="stage-header">
    <span class="slug mono">{roomName}</span>
    {#if state.connectionState !== 'connected'}
      <span class="connection">{state.connectionState}</span>
    {/if}
  </header>

  {#if hostLobby && lobbyOpen}
    <aside class="lobby-panel" aria-label="Lobby">
      <div class="lobby-heading">
        <h2>Lobby</h2>
        <span class="mono">{pending.length}</span>
      </div>
      {#if pending.length === 0}
        <p class="lobby-empty">No one is waiting.</p>
      {:else}
        <div class="lobby-list">
          {#each pending as request (request.id)}
            <div class="lobby-row">
              <span class="guest-name">{request.name}</span>
              <div class="lobby-actions">
                <button class="admit" type="button" onclick={() => void onadmit(request.id)}>
                  Admit
                </button>
                <button type="button" onclick={() => void ondeny(request.id)}>Deny</button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
      {#if lobbyError}<p class="lobby-error" role="alert">{lobbyError}</p>{/if}
    </aside>
  {/if}

  {#if focusParticipant && focusParticipant.screenShareTrack}
    <section class="focus-layout" aria-label="Meeting participants">
      <article class="focus-pane" data-testid="focus-pane">
        <!-- Live screen share does not have a caption track. -->
        <!-- svelte-ignore a11y_media_has_caption -->
        <video
          use:attachTrack={focusParticipant.screenShareTrack}
          autoplay
          playsinline
          muted={focusParticipant.isLocal}
          aria-label={`${focusParticipant.name}'s screen share`}
        ></video>
        <div class="focus-label">
          <span>{focusParticipant.name}</span>
          <span class="mono">Screen</span>
        </div>
      </article>

      <div class="camera-rail" aria-label="Participant cameras">
        {#each state.participants as participant (participant.identity)}
          {@render participantTile(participant)}
        {/each}
      </div>
    </section>
  {:else}
    <section
      class="grid"
      aria-label="Meeting participants"
      data-count={state.participants.length}
      style={`--grid-columns: ${gridColumns}; --grid-rows: ${gridRows}`}
    >
      {#each state.participants as participant (participant.identity)}
        {@render participantTile(participant)}
      {/each}
    </section>
  {/if}

  <ControlBar
    {state}
    {onleave}
    showLobbyControl={hostLobby}
    lobbyCount={pending.length}
    {lobbyOpen}
    ontogglelobby={() => (lobbyOpen = !lobbyOpen)}
  />
</main>

<style>
  .stage {
    min-height: 100dvh;
    padding: 14px 12px 64px;
    color: var(--text);
    background: var(--stage);
  }

  .stage-header {
    display: flex;
    height: 28px;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 8px;
  }

  .slug {
    padding: 2px 7px;
    color: var(--text);
    font-size: 11px;
    line-height: 18px;
    border: 1px solid var(--border-d);
    border-radius: 999px;
  }

  .connection {
    color: var(--text-2);
    font-size: 12px;
    text-transform: lowercase;
  }

  .lobby-panel {
    position: fixed;
    z-index: 15;
    top: 50px;
    right: 12px;
    width: min(280px, calc(100vw - 24px));
    padding: 12px;
    color: var(--text);
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
  }

  .lobby-heading,
  .lobby-row,
  .lobby-actions {
    display: flex;
    align-items: center;
  }

  .lobby-heading,
  .lobby-row {
    justify-content: space-between;
    gap: 8px;
  }

  .lobby-heading {
    margin-bottom: 8px;
  }

  .lobby-heading h2,
  .lobby-empty,
  .lobby-error {
    margin: 0;
  }

  .lobby-heading h2 {
    font-size: 13px;
    font-weight: 550;
  }

  .lobby-heading span {
    color: var(--text-2);
    font-size: 11px;
  }

  .lobby-list {
    display: grid;
    gap: 4px;
  }

  .lobby-row {
    min-height: 36px;
    padding: 4px;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  .guest-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .lobby-actions {
    flex: none;
    gap: 4px;
  }

  .lobby-actions button {
    height: 24px;
    padding: 1px 7px;
    color: var(--text-2);
    background: transparent;
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  .lobby-actions button:hover {
    color: var(--text);
    background: var(--panel);
  }

  .lobby-actions button.admit {
    color: white;
    background: var(--accent-d);
    border-color: var(--accent-d);
  }

  .lobby-empty,
  .lobby-error {
    color: var(--text-2);
    font-size: 12px;
  }

  .lobby-error {
    margin-top: 8px;
    color: var(--rec);
  }

  .grid {
    display: grid;
    height: calc(100dvh - 114px);
    grid-template-columns: repeat(var(--grid-columns), minmax(0, 1fr));
    grid-template-rows: repeat(var(--grid-rows), minmax(0, 1fr));
    gap: 8px;
  }

  .grid[data-count='1'] {
    width: min(100%, 960px);
    margin-inline: auto;
  }

  .tile {
    position: relative;
    display: grid;
    min-height: 0;
    overflow: hidden;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-tile);
    transition: border-color var(--motion-fast);
  }

  .tile.speaking {
    border-color: var(--accent-d);
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

  .tile video.mirrored {
    transform: scaleX(-1);
  }

  .placeholder {
    display: grid;
    place-items: center;
    color: var(--text-2);
    font-size: 18px;
  }

  .name-label,
  .focus-label {
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

  .focus-layout {
    display: grid;
    height: calc(100dvh - 114px);
    grid-template-columns: minmax(0, 1fr) 160px;
    gap: 8px;
  }

  .focus-pane {
    position: relative;
    display: grid;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
    background: var(--stage);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-tile);
  }

  .focus-pane video {
    width: 100%;
    height: 100%;
    object-fit: contain;
  }

  .focus-label {
    color: var(--text);
  }

  .focus-label .mono {
    color: var(--text-2);
    font-size: 10px;
    text-transform: uppercase;
  }

  .camera-rail {
    display: grid;
    min-height: 0;
    grid-auto-rows: max-content;
    gap: 8px;
    overflow-y: auto;
  }

  .camera-rail .tile {
    min-height: 90px;
    aspect-ratio: 16 / 9;
  }

  @media (max-width: 720px) {
    .grid {
      height: auto;
      grid-template-columns: 1fr;
      grid-template-rows: none;
      grid-auto-rows: minmax(180px, auto);
    }

    .grid .tile {
      aspect-ratio: 16 / 9;
    }

    .focus-layout {
      height: auto;
      grid-template-columns: 1fr;
    }

    .focus-pane {
      min-height: 50dvh;
    }

    .camera-rail {
      grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
      overflow: visible;
    }
  }
</style>
