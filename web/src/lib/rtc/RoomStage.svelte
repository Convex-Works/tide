<script lang="ts">
  import type { Track } from 'livekit-client';
  import type { LobbyRequestInfo } from '$lib/api/types.gen';
  import ChatPanel from '$lib/ui/ChatPanel.svelte';
  import ControlBar from '$lib/ui/ControlBar.svelte';
  import ParticipantPanel from '$lib/ui/ParticipantPanel.svelte';
  import ParticipantTile from './ParticipantTile.svelte';
  import type { RoomState } from './room.svelte';

  let {
    rtc,
    roomName,
    onleave,
    isOwner = false,
    pending = [],
    peopleOpen = $bindable(false),
    lobbyError = '',
    onadmit = () => undefined,
    ondeny = () => undefined
  }: {
    rtc: RoomState;
    roomName: string;
    onleave: () => void;
    isOwner?: boolean;
    pending?: LobbyRequestInfo[];
    peopleOpen?: boolean;
    lobbyError?: string;
    onadmit?: (id: string) => void | Promise<void>;
    ondeny?: (id: string) => void | Promise<void>;
  } = $props();

  let focusParticipant = $derived(
    rtc.participants.find((participant) => participant.screenShareTrack)
  );
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
</script>

<main class="stage">
  <header class="stage-header">
    <div class="room-status">
      <span class="slug mono">{roomName}</span>
      {#if rtc.isRecording}<span class="rec-chip mono" data-testid="recording-chip">REC</span>{/if}
    </div>
    {#if rtc.connectionState !== 'connected'}
      <span class="connection">{rtc.connectionState}</span>
    {/if}
  </header>

  {#if peopleOpen}
    <ParticipantPanel {rtc} slug={roomName} {isOwner} {pending} {lobbyError} {onadmit} {ondeny} />
  {:else if chatOpen}
    <ChatPanel {rtc} />
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
          muted
          aria-label={`${focusParticipant.name}'s screen share`}
        ></video>
        <div class="focus-label">
          <span>{focusParticipant.name}</span>
          <span class="mono">Screen</span>
        </div>
      </article>

      <div class="camera-rail" aria-label="Participant cameras">
        {#each rtc.participants as participant (participant.identity)}
          <ParticipantTile
            {participant}
            fit="width"
            speaking={rtc.activeSpeakerIdentities.includes(participant.identity)}
          />
        {/each}
      </div>
    </section>
  {:else if view === 'speaker' && promoted && rtc.participants.length > 1}
    <section class="focus-layout" aria-label="Meeting participants">
      <div class="speaker-pane" data-testid="speaker-pane">
        <ParticipantTile
          participant={promoted}
          fit="contain"
          speaking={rtc.activeSpeakerIdentities.includes(promoted.identity)}
        />
      </div>
      <div class="camera-rail" aria-label="Participant cameras">
        <!-- The promoted participant renders only in the pane: their audio
             elements live on the tile, so a rail copy would double audio. -->
        {#each rtc.participants.filter((p) => p.identity !== promoted.identity) as participant (participant.identity)}
          <ParticipantTile
            {participant}
            fit="width"
            speaking={rtc.activeSpeakerIdentities.includes(participant.identity)}
          />
        {/each}
      </div>
    </section>
  {:else}
    <section
      class="grid"
      aria-label="Meeting participants"
      data-count={rtc.participants.length}
      style={`--grid-columns: ${gridColumns}; --grid-rows: ${gridRows}`}
    >
      {#each rtc.participants as participant (participant.identity)}
        <ParticipantTile
          {participant}
          fit="contain"
          speaking={rtc.activeSpeakerIdentities.includes(participant.identity)}
        />
      {/each}
    </section>
  {/if}

  <ControlBar
    {rtc}
    {onleave}
    {peopleOpen}
    {chatOpen}
    {unreadChat}
    {isOwner}
    {view}
    roomSlug={roomName}
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

  .slug {
    padding: 2px 7px;
    color: var(--text);
    font-size: 11px;
    line-height: 18px;
    border: 1px solid var(--border-d);
    border-radius: 999px;
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

  .grid {
    display: grid;
    height: calc(100dvh - 128px);
    grid-template-columns: repeat(var(--grid-columns), minmax(0, 1fr));
    grid-template-rows: repeat(var(--grid-rows), minmax(0, 1fr));
    gap: 8px;
  }

  .grid[data-count='1'] {
    width: min(100%, 960px);
    margin-inline: auto;
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

  .focus-layout {
    display: grid;
    height: calc(100dvh - 128px);
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

  .speaker-pane {
    display: grid;
    min-width: 0;
    min-height: 0;
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

  @media (max-width: 720px) {
    .grid {
      height: auto;
      grid-template-columns: 1fr;
      grid-template-rows: none;
      grid-auto-rows: auto;
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
