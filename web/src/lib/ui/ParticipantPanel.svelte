<script lang="ts">
  import { MicrophoneSlash, VideoCameraSlash } from 'phosphor-svelte';
  import { kick, muteParticipant } from '$lib/api/client';
  import type { LobbyRequestInfo } from '$lib/api/types.gen';
  import type { ParticipantView, RoomState } from '$lib/rtc/room.svelte';

  let {
    rtc,
    slug,
    canManage = false,
    pending = [],
    lobbyError = '',
    onadmit = () => undefined,
    ondeny = () => undefined
  }: {
    rtc: RoomState;
    slug: string;
    canManage?: boolean;
    pending?: LobbyRequestInfo[];
    lobbyError?: string;
    onadmit?: (id: string) => void | Promise<void>;
    ondeny?: (id: string) => void | Promise<void>;
  } = $props();

  let moderationError = $state('');
  let busyIdentity = $state('');
  let confirmIdentity = $state('');

  async function mute(participant: ParticipantView): Promise<void> {
    moderationError = '';
    busyIdentity = participant.identity;
    try {
      await muteParticipant(slug, participant.identity);
    } catch (cause) {
      moderationError = cause instanceof Error ? cause.message : 'Could not mute this person.';
    } finally {
      busyIdentity = '';
    }
  }

  async function remove(participant: ParticipantView): Promise<void> {
    if (confirmIdentity !== participant.identity) {
      confirmIdentity = participant.identity;
      return;
    }
    moderationError = '';
    busyIdentity = participant.identity;
    try {
      await kick(slug, participant.identity);
      confirmIdentity = '';
    } catch (cause) {
      moderationError = cause instanceof Error ? cause.message : 'Could not remove this person.';
    } finally {
      busyIdentity = '';
    }
  }
</script>

<aside class="people-panel" aria-label="People">
  <header>
    <h2>People</h2>
    <span class="mono">{rtc.participants.length}</span>
  </header>

  {#if canManage && pending.length > 0}
    <section class="waiting" aria-label="Lobby">
      <div class="section-heading">
        <h3>Waiting</h3>
        <span class="mono">{pending.length}</span>
      </div>
      <div class="waiting-list">
        {#each pending as request (request.id)}
          <div class="waiting-row">
            <span class="participant-name">{request.name}</span>
            <div class="row-actions">
              <button class="admit" type="button" onclick={() => void onadmit(request.id)}>
                Admit
              </button>
              <button type="button" onclick={() => void ondeny(request.id)}>Deny</button>
            </div>
          </div>
        {/each}
      </div>
      {#if lobbyError}<p class="error" role="alert">{lobbyError}</p>{/if}
    </section>
  {/if}

  <div class="participant-list">
    {#each rtc.participants as participant (participant.identity)}
      <div class="participant-row" data-identity={participant.identity}>
        <div class="identity">
          <div class="name-line">
            <span class="participant-name">{participant.name}</span>
            {#if participant.isLocal}<span class="you mono">You</span>{/if}
            {#if rtc.isHostParticipant(participant)}<span class="host">Host</span>{/if}
          </div>
          <div class="media-state">
            {#if participant.micMuted}
              <MicrophoneSlash size={16} weight="regular" aria-label="Microphone off" />
            {/if}
            {#if participant.camMuted}
              <VideoCameraSlash size={16} weight="regular" aria-label="Camera off" />
            {/if}
          </div>
        </div>

        {#if canManage && !participant.isLocal}
          <div class="moderation-actions">
            <button
              type="button"
              disabled={busyIdentity === participant.identity || participant.micMuted}
              onclick={() => void mute(participant)}
            >
              Mute
            </button>
            <button
              class:confirm={confirmIdentity === participant.identity}
              type="button"
              disabled={busyIdentity === participant.identity}
              onclick={() => void remove(participant)}
            >
              {confirmIdentity === participant.identity ? 'Remove?' : 'Remove'}
            </button>
          </div>
        {/if}
      </div>
    {/each}
  </div>

  {#if moderationError}<p class="error" role="alert">{moderationError}</p>{/if}
</aside>

<style>
  .people-panel {
    position: fixed;
    z-index: 15;
    /* Below the stage header, which RoomStage measures. */
    top: var(--stage-panel-top, 50px);
    right: 12px;
    bottom: 78px;
    display: flex;
    width: min(300px, calc(100vw - 24px));
    padding: 12px;
    flex-direction: column;
    gap: 8px;
    overflow-y: auto;
    color: var(--text);
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
    animation: panel-in var(--motion-fast);
  }

  header,
  .section-heading,
  .participant-row,
  .identity,
  .name-line,
  .media-state,
  .waiting-row,
  .row-actions,
  .moderation-actions {
    display: flex;
    align-items: center;
  }

  header,
  .section-heading,
  .waiting-row,
  .participant-row {
    justify-content: space-between;
  }

  h2,
  h3,
  p {
    margin: 0;
  }

  h2 {
    color: var(--text-2);
    font-size: 12px;
    font-weight: 550;
  }

  h3 {
    color: var(--text-2);
    font-size: 12px;
    font-weight: 550;
  }

  header > span,
  .section-heading > span {
    color: var(--text-2);
    font-size: 11px;
  }

  .waiting {
    display: grid;
    padding-bottom: 8px;
    gap: 5px;
    border-bottom: 1px solid var(--border-d);
  }

  .waiting-list,
  .participant-list {
    display: grid;
    gap: 4px;
  }

  .waiting-row,
  .participant-row {
    min-height: 36px;
    padding: 4px 5px;
    gap: 6px;
    background: var(--panel-2);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  .identity {
    min-width: 0;
    flex: 1;
    justify-content: space-between;
    gap: 6px;
  }

  .name-line,
  .media-state,
  .row-actions,
  .moderation-actions {
    gap: 4px;
  }

  .participant-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .you,
  .host {
    flex: none;
    font-size: 10px;
  }

  .you {
    color: var(--text-2);
    text-transform: uppercase;
  }

  .host {
    padding: 0 4px;
    color: var(--accent-d);
    border: 1px solid color-mix(in srgb, var(--accent-d) 45%, transparent);
    border-radius: 999px;
  }

  .media-state {
    flex: none;
    color: var(--text-2);
  }

  button {
    height: 24px;
    padding: 1px 6px;
    color: var(--text-2);
    background: transparent;
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  button:hover:not(:disabled) {
    color: var(--text);
    background: var(--panel);
  }

  button:focus-visible {
    outline-color: var(--accent-d);
  }

  button:disabled {
    cursor: default;
    opacity: 0.45;
  }

  button.admit {
    color: white;
    background: var(--accent-d);
    border-color: var(--accent-d);
  }

  button.confirm {
    color: var(--rec);
    border-color: color-mix(in srgb, var(--rec) 45%, transparent);
  }

  .moderation-actions {
    flex: none;
    opacity: 0;
    transition: opacity var(--motion-fast);
  }

  .participant-row:hover .moderation-actions,
  .participant-row:focus-within .moderation-actions {
    opacity: 1;
  }

  .error {
    color: var(--rec);
    font-size: 11px;
  }

  @keyframes panel-in {
    from {
      opacity: 0;
      transform: translateX(6px);
    }
  }

  @media (hover: none) {
    .moderation-actions {
      opacity: 1;
    }
  }
</style>
