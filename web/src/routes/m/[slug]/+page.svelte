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
  import type {
    LobbyAdmittedSSE,
    LobbyRequestInfo,
    PublicRoomInfo
  } from '$lib/api/types.gen';
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
    | 'connected'
    | 'left';

  const rtc = new RoomState();
  const slug = $derived(page.params.slug);
  let meetingState = $state<MeetingState>('loading');
  let details = $state<PublicRoomInfo>();
  let name = $state('');
  let error = $state('');
  let isOwner = $state(false);
  let pending = $state<LobbyRequestInfo[]>([]);
  let lobbyOpen = $state(false);
  let lobbyError = $state('');
  let mediaOptions: PreJoinOptions | undefined;
  let closeWait: (() => void) | undefined;
  let closeHostLobby: (() => void) | undefined;

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
    lobbyOpen = false;
    setConnectionChrome('connected');
    meetingState = 'left';
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
    state={rtc}
    roomName={slug}
    onleave={leaveMeeting}
    hostLobby={isOwner}
    {pending}
    bind:lobbyOpen
    {lobbyError}
    onadmit={admit}
    ondeny={deny}
  />
{:else if meetingState === 'prejoin' && details}
  <PreJoin
    room={slug}
    bind:name
    {error}
    showRoom={false}
    heading={details.name}
    onjoin={join}
  />
{:else if meetingState === 'loading'}
  <main class="meeting-state" aria-live="polite"><p>Loading room…</p></main>
{:else if meetingState === 'missing'}
  <main class="meeting-state">
    <div class="wordmark">klisi</div>
    <h1>This room does not exist.</h1>
    <p>Check the meeting link and try again.</p>
  </main>
{:else if meetingState === 'load-error'}
  <main class="meeting-state">
    <div class="wordmark">klisi</div>
    <h1>Could not load the room.</h1>
    <p role="alert">{error}</p>
    <button type="button" onclick={() => void loadMeeting()}>Try again</button>
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
    <div class="wordmark">klisi</div>
    <h1>The host did not let you in.</h1>
    <p>You can close this page or ask the host for a new invitation.</p>
  </main>
{:else if meetingState === 'left'}
  <main class="meeting-state">
    <div class="wordmark">klisi</div>
    <h1>You left the meeting.</h1>
    <button class="primary" type="button" onclick={rejoin}>Rejoin</button>
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

  .meeting-state h1,
  .meeting-state p {
    margin: 0;
  }

  .meeting-state h1 {
    color: var(--ink);
    font-size: 18px;
    line-height: 24px;
    font-weight: 550;
  }

  .meeting-state p {
    max-width: 380px;
    font-size: 12px;
  }

  .wordmark {
    margin-bottom: 8px;
    color: var(--accent);
    font-size: 15px;
    font-weight: 550;
    letter-spacing: 0.02em;
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

  button {
    height: var(--control-height);
    padding: 3px 8px;
    color: var(--ink);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  button:hover {
    background: var(--surface-2);
  }

  button.primary {
    color: white;
    font-weight: 550;
    background: var(--accent);
    border-color: var(--accent);
  }

  button.primary:hover {
    background: var(--accent-hover);
    border-color: var(--accent-hover);
  }
</style>
