<script lang="ts">
  import { onDestroy } from 'svelte';
  import { VideoCamera } from 'phosphor-svelte';
  import { getDevToken } from '$lib/api/client';
  import RoomStage from '$lib/rtc/RoomStage.svelte';
  import { RoomState } from '$lib/rtc/room.svelte';

  const rtc = new RoomState();
  let room = $state('steel-thread');
  let name = $state('');
  let joined = $state(false);
  let joining = $state(false);
  let error = $state('');

  async function join(event: SubmitEvent) {
    event.preventDefault();
    error = '';
    joining = true;
    try {
      room = room.trim();
      name = name.trim();
      const response = await getDevToken(room, name);
      await rtc.connect(response.ws_url, response.token);
      joined = true;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not join the room. Try again.';
    } finally {
      joining = false;
    }
  }

  onDestroy(() => {
    void rtc.disconnect();
  });
</script>

<svelte:head>
  <title>klisi</title>
  <meta name="description" content="Lean self-hosted video meetings" />
</svelte:head>

{#if joined}
  <RoomStage state={rtc} roomName={room} />
{:else}
  <main class="join-shell">
    <form class="join-card" onsubmit={join}>
      <div class="brand">klisi</div>
      <h1>Join a room</h1>

      <label>
        <span>Room</span>
        <input class="mono" bind:value={room} name="room" autocomplete="off" required />
      </label>

      <label>
        <span>Name</span>
        <input bind:value={name} name="name" autocomplete="name" required />
      </label>

      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}

      <button type="submit" disabled={joining}>
        <VideoCamera size={16} weight="regular" aria-hidden="true" />
        {joining ? 'Joining…' : 'Join room'}
      </button>
    </form>
  </main>
{/if}

<style>
  .join-shell {
    display: grid;
    min-height: 100dvh;
    place-items: center;
    padding: 24px 12px;
    background: var(--paper);
  }

  .join-card {
    width: min(100%, 320px);
    padding: 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-card);
  }

  .brand {
    margin-bottom: 16px;
    color: var(--accent);
    font-size: 12px;
    font-weight: 550;
    letter-spacing: 0.02em;
  }

  h1 {
    margin: 0 0 16px;
    font-size: 18px;
    line-height: 24px;
    font-weight: 550;
  }

  label {
    display: grid;
    gap: 4px;
    margin-bottom: 8px;
  }

  label span {
    color: var(--ink-2);
    font-size: 12px;
  }

  input {
    width: 100%;
    height: var(--control-height);
    padding: 3px 7px;
    color: var(--ink);
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
    transition: border-color var(--motion-fast), background var(--motion-fast);
  }

  input:hover {
    border-color: var(--ink-2);
  }

  button {
    display: flex;
    width: 100%;
    height: var(--control-height);
    align-items: center;
    justify-content: center;
    gap: 6px;
    margin-top: 12px;
    padding: 3px 8px;
    color: white;
    font-weight: 550;
    background: var(--accent);
    border: 1px solid var(--accent);
    border-radius: var(--radius-control);
    transition: background var(--motion-fast), border-color var(--motion-fast);
  }

  button:hover:not(:disabled) {
    background: var(--accent-hover);
    border-color: var(--accent-hover);
  }

  button:disabled {
    cursor: wait;
    opacity: 0.65;
  }

  .error {
    margin: 8px 0 0;
    color: #a22c32;
    font-size: 12px;
  }
</style>
