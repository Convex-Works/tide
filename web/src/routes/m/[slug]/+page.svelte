<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { page } from '$app/state';
  import {
    ApiError,
    approveLobby,
    denyLobby,
    joinRoom,
    listRooms,
    lobbyWait,
    me,
    roomInfo,
    roomLobby
  } from '$lib/api/client';
  import type { LobbyAdmittedSSE, LobbyRequestInfo, PublicRoomInfo } from '$lib/api/types.gen';
  import { ConnectionState, DisconnectReason } from 'livekit-client';
  import PreJoin from '$lib/rtc/PreJoin.svelte';
  import RoomStage from '$lib/rtc/RoomStage.svelte';
  import { setConnectionChrome } from '$lib/rtc/connection.svelte';
  import { RoomState, type PreJoinOptions } from '$lib/rtc/room.svelte';

  type MeetingState =
    | 'loading'
    | 'missing'
    | 'load-error'
    | 'prejoin'
    | 'joining'
    | 'waiting'
    | 'denied'
    | 'expired'
    | 'connected'
    | 'removed'
    | 'disconnected'
    | 'left';

  const rtc = new RoomState();
  const slug = $derived(page.params.slug ?? '');
  let meetingState = $state<MeetingState>('loading');
  let details = $state<PublicRoomInfo>();
  let name = $state('');
  let error = $state('');
  let isOwner = $state(false);
  let pending = $state<LobbyRequestInfo[]>([]);
  let peopleOpen = $state(false);
  let lobbyError = $state('');
  let mediaOptions: PreJoinOptions | undefined;
  let closeWait: (() => void) | undefined;
  let closeHostLobby: (() => void) | undefined;

  $effect(() => {
    if (meetingState !== 'connected') return;
    if (rtc.wasRemoved) {
      handleRemoved();
      return;
    }
    // Any other terminal disconnect (server shutdown, room closed, duplicate
    // identity…) must land somewhere recoverable instead of a dead stage.
    if (
      rtc.connectionState === ConnectionState.Disconnected &&
      rtc.disconnectReason !== DisconnectReason.CLIENT_INITIATED
    ) {
      leaveMeetingChrome();
      meetingState = 'disconnected';
    }
  });

  async function loadMeeting(): Promise<void> {
    meetingState = 'loading';
    error = '';
    try {
      details = await roomInfo(slug);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404) {
        meetingState = 'missing';
        return;
      }
      error = cause instanceof Error ? cause.message : 'Could not load the room. Reload the page.';
      meetingState = 'load-error';
      return;
    }

    try {
      const user = await me();
      name = user.name;
      const ownedRooms = await listRooms();
      isOwner = ownedRooms.some((room) => room.slug === slug);
    } catch {
      isOwner = false;
    }
    meetingState = 'prejoin';
  }

  async function connect(admission: LobbyAdmittedSSE, options: PreJoinOptions): Promise<void> {
    closeWait?.();
    closeWait = undefined;
    meetingState = 'joining';
    try {
      await rtc.connect(admission.ws_url, admission.token, options);
      meetingState = 'connected';
      if (isOwner) subscribeToHostLobby();
    } catch {
      error = 'Could not connect to the meeting. Check your connection and try again.';
      setConnectionChrome('connected');
      meetingState = 'prejoin';
    }
  }

  async function join(options: PreJoinOptions): Promise<void> {
    error = '';
    mediaOptions = options;
    meetingState = 'joining';
    try {
      const response = await joinRoom(slug, options.name.trim());
      if (response.status === 'admitted' && response.token && response.ws_url) {
        await connect({ token: response.token, ws_url: response.ws_url }, options);
        return;
      }
      if (response.status === 'waiting' && response.request_id) {
        meetingState = 'waiting';
        closeWait = lobbyWait(response.request_id, {
          admitted: (admission) => {
            if (mediaOptions) void connect(admission, mediaOptions);
          },
          denied: () => {
            closeWait?.();
            closeWait = undefined;
            meetingState = 'denied';
          },
          expired: () => {
            closeWait?.();
            closeWait = undefined;
            meetingState = 'expired';
          },
          error: () => {
            // The wait stream died for good (request expired while the tab
            // was suspended); leave "waiting" so the guest can ask again.
            closeWait?.();
            closeWait = undefined;
            meetingState = 'expired';
          }
        });
        return;
      }
      throw new Error('The server returned an invalid join response. Try again.');
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not join the room. Try again.';
      setConnectionChrome('connected');
      meetingState = 'prejoin';
    }
  }

  function subscribeToHostLobby(): void {
    closeHostLobby?.();
    closeHostLobby = roomLobby(slug, (event) => {
      pending = event.requests;
    });
  }

  async function admit(id: string): Promise<void> {
    lobbyError = '';
    try {
      await approveLobby(id);
    } catch (cause) {
      lobbyError = cause instanceof Error ? cause.message : 'Could not admit the guest. Try again.';
    }
  }

  async function deny(id: string): Promise<void> {
    lobbyError = '';
    try {
      await denyLobby(id);
    } catch (cause) {
      lobbyError = cause instanceof Error ? cause.message : 'Could not deny the guest. Try again.';
    }
  }

  function leaveMeeting(): void {
    closeHostLobby?.();
    closeHostLobby = undefined;
    pending = [];
    peopleOpen = false;
    setConnectionChrome('connected');
    meetingState = 'left';
  }

  function handleRemoved(): void {
    leaveMeetingChrome();
    meetingState = 'removed';
  }

  function leaveMeetingChrome(): void {
    closeHostLobby?.();
    closeHostLobby = undefined;
    pending = [];
    peopleOpen = false;
    setConnectionChrome('connected');
  }

  function rejoin(): void {
    error = '';
    lobbyError = '';
    mediaOptions = undefined;
    setConnectionChrome('connected');
    meetingState = 'prejoin';
  }

  onMount(() => void loadMeeting());
  onDestroy(() => {
    closeWait?.();
    closeHostLobby?.();
    void rtc
      .disconnect()
      .catch(() => undefined)
      .finally(() => setConnectionChrome('connected'));
  });
</script>

<svelte:head>
  <title>{details ? `${details.name} · klisi` : 'klisi'}</title>
  <meta name="description" content="Join a klisi meeting" />
</svelte:head>

{#if meetingState === 'connected'}
  <RoomStage
    {rtc}
    roomName={slug}
    onleave={leaveMeeting}
    {isOwner}
    {pending}
    bind:peopleOpen
    {lobbyError}
    onadmit={admit}
    ondeny={deny}
  />
{:else if meetingState === 'prejoin' && details}
  <PreJoin room={slug} bind:name {error} showRoom={false} heading={details.name} onjoin={join} />
{:else if meetingState === 'loading'}
  <main class="meeting-state" aria-live="polite"><p>Loading room…</p></main>
{:else if meetingState === 'missing'}
  <main class="meeting-state">
    <p class="state-message">This room does not exist.</p>
    <a class="state-action" href="/">Go to dashboard</a>
  </main>
{:else if meetingState === 'load-error'}
  <main class="meeting-state">
    <p class="state-message" role="alert">{error}</p>
    <button class="state-action" type="button" onclick={() => void loadMeeting()}>
      Try again
    </button>
  </main>
{:else if meetingState === 'joining'}
  <main class="meeting-state" aria-live="polite">
    <span class="slug mono">{slug}</span>
    <p>Joining…</p>
  </main>
{:else if meetingState === 'waiting'}
  <main class="meeting-state" aria-live="polite">
    <span class="waiting-dot" aria-hidden="true"></span>
    <h1>Waiting for the host to let you in.</h1>
    <span class="slug mono">{slug}</span>
  </main>
{:else if meetingState === 'denied'}
  <main class="meeting-state">
    <p class="state-message">The host did not let you in.</p>
    <a class="state-action" href="/">Go to dashboard</a>
  </main>
{:else if meetingState === 'expired'}
  <main class="meeting-state">
    <p class="state-message">Your lobby request expired.</p>
    <button class="state-action" type="button" onclick={rejoin}>Try again</button>
  </main>
{:else if meetingState === 'left'}
  <main class="meeting-state">
    <p class="state-message">You left the meeting.</p>
    <button class="state-action" type="button" onclick={rejoin}>Rejoin</button>
  </main>
{:else if meetingState === 'removed'}
  <main class="meeting-state">
    <p class="state-message">You were removed from the meeting.</p>
    <button class="state-action" type="button" onclick={rejoin}>Request to rejoin</button>
  </main>
{:else if meetingState === 'disconnected'}
  <main class="meeting-state">
    <p class="state-message">
      {rtc.disconnectReason === DisconnectReason.DUPLICATE_IDENTITY
        ? 'This meeting was opened from another tab or device.'
        : 'The connection to the meeting was lost.'}
    </p>
    <button class="state-action" type="button" onclick={rejoin}>Rejoin</button>
  </main>
{/if}

<style>
  .meeting-state {
    display: grid;
    min-height: 100dvh;
    padding: 24px 12px;
    align-content: center;
    justify-items: center;
    gap: 8px;
    color: var(--ink-2);
    text-align: center;
    background: var(--paper);
  }

  .meeting-state p {
    margin: 0;
  }

  .state-message {
    max-width: 380px;
    color: var(--ink);
    font-size: 13px;
    line-height: 20px;
    font-weight: 550;
  }

  .meeting-state > p:not(.state-message) {
    max-width: 380px;
    font-size: 12px;
  }

  .slug {
    padding: 2px 7px;
    color: var(--ink-2);
    font-size: 11px;
    line-height: 18px;
    border: 1px solid var(--border);
    border-radius: 999px;
  }

  .waiting-dot {
    width: 8px;
    height: 8px;
    margin-bottom: 4px;
    background: var(--warn);
    border-radius: 999px;
  }

  button,
  .state-action {
    display: inline-flex;
    height: var(--control-height);
    align-items: center;
    padding: 3px 8px;
    color: white;
    font-weight: 550;
    text-decoration: none;
    background: var(--accent);
    border: 1px solid var(--accent);
    border-radius: var(--radius-control);
  }

  button:hover,
  .state-action:hover {
    background: var(--accent-hover);
    border-color: var(--accent-hover);
  }
</style>
