<script lang="ts">
  import { onDestroy } from 'svelte';
  import { getDevToken } from '$lib/api/client';
  import PreJoin from '$lib/rtc/PreJoin.svelte';
  import RoomStage from '$lib/rtc/RoomStage.svelte';
  import { setConnectionChrome } from '$lib/rtc/connection.svelte';
  import { RoomState, type PreJoinOptions } from '$lib/rtc/room.svelte';

  type MeetingState = 'prejoin' | 'connecting' | 'stage';

  const rtc = new RoomState();
  let room = $state('steel-thread');
  let name = $state('');
  let meetingState = $state<MeetingState>('prejoin');
  let error = $state('');

  async function join(options: PreJoinOptions): Promise<void> {
    error = '';
    meetingState = 'connecting';

    try {
      room = room.trim();
      name = options.name.trim();
      const response = await getDevToken(room, name);
      await rtc.connect(response.ws_url, response.token, options);
      meetingState = 'stage';
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not join the room. Try again.';
      setConnectionChrome('connected');
      meetingState = 'prejoin';
    }
  }

  function returnToPrejoin(): void {
    setConnectionChrome('connected');
    meetingState = 'prejoin';
  }

  onDestroy(() => {
    void rtc
      .disconnect()
      .catch(() => undefined)
      .finally(() => setConnectionChrome('connected'));
  });
</script>

<svelte:head>
  <title>klisi</title>
  <meta name="description" content="Lean self-hosted video meetings" />
</svelte:head>

{#if !import.meta.env.DEV && import.meta.env.VITE_KLISI_TEST !== 'true'}
  <main class="unavailable">Not available.</main>
{:else if meetingState === 'stage'}
  <RoomStage {rtc} roomName={room} onleave={returnToPrejoin} />
{:else if meetingState === 'connecting'}
  <main class="connecting" aria-live="polite">
    <span class="mono">{room}</span>
    <p>Joining…</p>
  </main>
{:else}
  <PreJoin bind:room bind:name {error} onjoin={join} />
{/if}

<style>
  .connecting,
  .unavailable {
    display: grid;
    min-height: 100dvh;
    align-content: center;
    justify-items: center;
    gap: 8px;
    color: var(--ink-2);
    background: var(--paper);
  }

  .connecting span {
    padding: 2px 7px;
    color: var(--ink);
    font-size: 11px;
    border: 1px solid var(--border);
    border-radius: 999px;
  }

  .connecting p {
    margin: 0;
    font-size: 12px;
  }
</style>
