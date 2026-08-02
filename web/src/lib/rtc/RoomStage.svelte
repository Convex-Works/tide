<script lang="ts">
  import type { LobbyRequestInfo } from '$lib/api/types.gen';
  import ChatPanel from '$lib/ui/ChatPanel.svelte';
  import ControlBar from '$lib/ui/ControlBar.svelte';
  import ParticipantPanel from '$lib/ui/ParticipantPanel.svelte';
  import ParticipantTile from './ParticipantTile.svelte';
  import RemoteAudioRenderer from './RemoteAudioRenderer.svelte';
  import { attachMediaTrack } from './mediaElement';
  import type { RoomState } from './room.svelte';

  let {
    rtc,
    roomSlug,
    onleave,
    canManage = false,
    pending = [],
    peopleOpen = $bindable(false),
    lobbyError = '',
    onadmit = () => undefined,
    ondeny = () => undefined
  }: {
    rtc: RoomState;
    roomSlug: string;
    onleave: () => void;
    canManage?: boolean;
    pending?: LobbyRequestInfo[];
    peopleOpen?: boolean;
    lobbyError?: string;
    onadmit?: (id: string) => void | Promise<void>;
    ondeny?: (id: string) => void | Promise<void>;
  } = $props();

  let focusParticipant = $derived(rtc.participants.find((participant) => participant.screenShare));
  let view = $state<'grid' | 'speaker'>('grid');
  let lastSpeakerIdentity = $state<string>();

  // Speaker view promotes the most recent remote active speaker; sticky
  // through silence so the pane doesn't flicker between turns. The local
  // participant never self-promotes — you stay in the rail.
  $effect(() => {
    const speaking = rtc.activeSpeakerIdentities.find((identity) =>
      rtc.participants.some(
        (participant) => participant.identity === identity && !participant.isLocal
      )
    );
    if (speaking) lastSpeakerIdentity = speaking;
  });

  let promoted = $derived(
    rtc.participants.find((participant) => participant.identity === lastSpeakerIdentity) ??
      rtc.participants.find((participant) => !participant.isLocal) ??
      rtc.participants.at(0)
  );
  let gridColumns = $derived(
    Math.min(3, Math.max(1, Math.ceil(Math.sqrt(rtc.participants.length))))
  );
  let gridRows = $derived(Math.max(1, Math.ceil(rtc.participants.length / gridColumns)));
  let layoutMode = $derived(
    focusParticipant
      ? 'screen'
      : view === 'speaker' && rtc.participants.length > 1
        ? 'speaker'
        : 'grid'
  );
  let speakerRailCount = $derived(
    Math.max(1, rtc.participants.filter((participant) => participant !== promoted).length)
  );
  let previousPendingCount = 0;
  let chatOpen = $state(false);
  let unreadChat = $state(0);
  let seenChatRevision = 0;

  $effect(() => {
    if (pending.length > 0 && previousPendingCount === 0) {
      peopleOpen = true;
      chatOpen = false;
    }
    previousPendingCount = pending.length;
  });

  $effect(() => {
    const revision = rtc.chatRevision;
    if (revision > seenChatRevision && !chatOpen) {
      const added = Math.min(revision - seenChatRevision, rtc.chat.length);
      unreadChat += rtc.chat.slice(-added).filter((message) => !message.mine).length;
    }
    if (chatOpen) unreadChat = 0;
    seenChatRevision = revision;
  });

  function togglePeople(): void {
    peopleOpen = !peopleOpen;
    if (peopleOpen) chatOpen = false;
  }

  function toggleChat(): void {
    chatOpen = !chatOpen;
    if (chatOpen) {
      peopleOpen = false;
      unreadChat = 0;
    }
  }

  function railRow(identity: string): number {
    return (
      rtc.participants
        .filter((participant) => participant.identity !== promoted?.identity)
        .findIndex((participant) => participant.identity === identity) + 1
    );
  }
</script>

<main class="stage">
  <header class="stage-header">
    <div class="room-status">
      {#if rtc.isRecording}<span class="rec-chip mono" data-testid="recording-chip">REC</span>{/if}
    </div>
    {#if rtc.connectionState !== 'connected'}
      <span class="connection">{rtc.connectionState}</span>
    {/if}
  </header>

  {#if peopleOpen}
    <ParticipantPanel {rtc} slug={roomSlug} {canManage} {pending} {lobbyError} {onadmit} {ondeny} />
  {:else if chatOpen}
    <ChatPanel {rtc} />
  {/if}

  <section
    class="media-layout"
    class:grid-mode={layoutMode === 'grid'}
    class:speaker-mode={layoutMode === 'speaker'}
    class:screen-mode={layoutMode === 'screen'}
    aria-label="Meeting participants"
    data-layout={layoutMode}
  >
    {#if focusParticipant?.screenShare}
      <article class="focus-pane" data-testid="focus-pane">
        {#key focusParticipant.screenShare.publicationSid}
          <!-- Live screen share does not have a caption track. -->
          <video
            use:attachMediaTrack={focusParticipant.screenShare}
            autoplay
            playsinline
            muted
            aria-label={`${focusParticipant.name}'s screen share`}
          ></video>
        {/key}
        <div class="focus-label">
          <span>{focusParticipant.name}</span>
          <span class="mono">Screen</span>
        </div>
      </article>
    {/if}

    <div
      class="participant-list"
      aria-label="Participant cameras"
      data-count={rtc.participants.length}
      data-testid={layoutMode === 'speaker' ? 'speaker-pane' : undefined}
      style={`--grid-columns: ${gridColumns}; --grid-rows: ${gridRows}; --speaker-rows: ${speakerRailCount}`}
    >
      {#each rtc.participants as participant (participant.identity)}
        {@const isPromoted =
          layoutMode === 'speaker' &&
          rtc.participants.length > 1 &&
          participant.identity === promoted?.identity}
        <ParticipantTile
          {participant}
          fit={layoutMode === 'grid' || isPromoted ? 'contain' : 'width'}
          speaking={rtc.activeSpeakerIdentities.includes(participant.identity)}
          promoted={isPromoted}
          rail={layoutMode === 'speaker' && !isPromoted}
          railRow={railRow(participant.identity)}
        />
      {/each}
    </div>
  </section>

  <RemoteAudioRenderer participants={rtc.participants} />

  <ControlBar
    {rtc}
    {onleave}
    {peopleOpen}
    {chatOpen}
    {unreadChat}
    {canManage}
    {view}
    {roomSlug}
    ontogglepeople={togglePeople}
    ontogglechat={toggleChat}
    ontoggleview={() => (view = view === 'grid' ? 'speaker' : 'grid')}
  />
</main>

<style>
  .stage {
    --focus-ring: var(--accent-d);

    min-height: 100dvh;
    padding: 14px 12px 78px;
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

  .room-status {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .rec-chip {
    padding: 1px 5px;
    color: white;
    font-size: 10px;
    line-height: 16px;
    letter-spacing: 0.06em;
    background: var(--rec);
    border-radius: 999px;
  }

  .connection {
    color: var(--text-2);
    font-size: 12px;
    text-transform: lowercase;
  }

  .media-layout {
    height: calc(100dvh - 128px);
    min-height: 0;
  }

  .participant-list {
    min-width: 0;
    min-height: 0;
  }

  .grid-mode .participant-list {
    display: grid;
    height: 100%;
    grid-template-columns: repeat(var(--grid-columns), minmax(0, 1fr));
    grid-template-rows: repeat(var(--grid-rows), minmax(0, 1fr));
    gap: 8px;
  }

  .grid-mode .participant-list[data-count='1'] {
    width: min(100%, 960px);
    margin-inline: auto;
  }

  .screen-mode {
    display: grid;
    grid-template-areas: 'focus rail';
    grid-template-columns: minmax(0, 1fr) 160px;
    gap: 8px;
  }

  .screen-mode .participant-list {
    display: grid;
    min-height: 0;
    grid-area: rail;
    grid-auto-rows: max-content;
    gap: 8px;
    overflow-y: auto;
  }

  .speaker-mode .participant-list {
    display: grid;
    height: 100%;
    grid-template-columns: minmax(0, 1fr) 160px;
    grid-template-rows: repeat(var(--speaker-rows), minmax(0, 1fr));
    gap: 8px;
  }

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

  .focus-pane {
    position: relative;
    display: grid;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
    background: var(--stage);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-tile);
    grid-area: focus;
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

  @media (max-width: 720px) {
    .media-layout {
      height: auto;
    }

    .grid-mode .participant-list,
    .speaker-mode .participant-list,
    .screen-mode .participant-list {
      grid-template-columns: 1fr;
      grid-template-rows: none;
      grid-auto-rows: auto;
    }

    .screen-mode {
      grid-template-columns: 1fr;
      grid-template-areas:
        'focus'
        'rail';
    }

    .focus-pane {
      min-height: 50dvh;
    }

    .screen-mode .participant-list {
      grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
      overflow: visible;
    }

    :global(.speaker-mode .cell) {
      grid-row: auto;
      grid-column: 1;
    }
  }
</style>
