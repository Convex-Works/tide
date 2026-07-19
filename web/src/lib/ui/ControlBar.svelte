<script lang="ts">
  import {
    Microphone,
    MicrophoneSlash,
    PhoneDisconnect,
    Screencast,
    VideoCamera,
    VideoCameraSlash
  } from 'phosphor-svelte';
  import type { RoomState } from '$lib/rtc/room.svelte';

  let { state, onleave }: { state: RoomState; onleave: () => void } = $props();

  async function leave(): Promise<void> {
    await state.leave().catch(() => undefined);
    onleave();
  }

  async function run(action: () => Promise<void>): Promise<void> {
    try {
      await action();
    } catch {
      // Browser media pickers may be cancelled without changing meeting state.
    }
  }
</script>

<nav class="control-bar" aria-label="Meeting controls">
  <button
    type="button"
    class:off={!state.micEnabled}
    aria-label={state.micEnabled ? 'Mute microphone' : 'Unmute microphone'}
    aria-pressed={state.micEnabled}
    title={state.micEnabled ? 'Mute microphone' : 'Unmute microphone'}
    onclick={() => void run(() => state.toggleMic())}
  >
    {#if state.micEnabled}
      <Microphone size={16} weight="regular" aria-hidden="true" />
    {:else}
      <MicrophoneSlash size={16} weight="regular" aria-hidden="true" />
    {/if}
  </button>

  <button
    type="button"
    class:off={!state.camEnabled}
    aria-label={state.camEnabled ? 'Turn camera off' : 'Turn camera on'}
    aria-pressed={state.camEnabled}
    title={state.camEnabled ? 'Turn camera off' : 'Turn camera on'}
    onclick={() => void run(() => state.toggleCam())}
  >
    {#if state.camEnabled}
      <VideoCamera size={16} weight="regular" aria-hidden="true" />
    {:else}
      <VideoCameraSlash size={16} weight="regular" aria-hidden="true" />
    {/if}
  </button>

  <button
    type="button"
    class:active={state.screenShareEnabled}
    aria-label={state.screenShareEnabled ? 'Stop sharing' : 'Share screen'}
    aria-pressed={state.screenShareEnabled}
    title={state.screenShareEnabled ? 'Stop sharing' : 'Share screen'}
    onclick={() => void run(() => state.toggleScreenShare())}
  >
    <Screencast size={16} weight="regular" aria-hidden="true" />
  </button>

  <span class="separator" aria-hidden="true"></span>

  <button
    type="button"
    class="leave"
    aria-label="Leave room"
    title="Leave room"
    onclick={() => void leave()}
  >
    <PhoneDisconnect size={16} weight="regular" aria-hidden="true" />
  </button>
</nav>

<style>
  .control-bar {
    position: fixed;
    z-index: 20;
    bottom: 16px;
    left: 50%;
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 5px;
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
    transform: translateX(-50%);
  }

  button {
    display: grid;
    width: 32px;
    height: var(--control-height);
    padding: 0;
    place-items: center;
    color: var(--text);
    background: transparent;
    border: 1px solid transparent;
    border-radius: var(--radius-control);
    transition:
      color var(--motion-fast),
      background var(--motion-fast),
      border-color var(--motion-fast);
  }

  button:hover {
    background: var(--panel-2);
    border-color: var(--border-d);
  }

  button:focus-visible {
    outline-color: var(--accent-d);
  }

  button.off {
    color: var(--rec);
    background: color-mix(in srgb, var(--rec) 10%, transparent);
  }

  button.active {
    color: var(--accent-d);
    background: color-mix(in srgb, var(--accent-d) 12%, transparent);
  }

  button.leave {
    color: var(--rec);
  }

  button.leave:hover {
    background: color-mix(in srgb, var(--rec) 12%, transparent);
    border-color: color-mix(in srgb, var(--rec) 32%, transparent);
  }

  .separator {
    width: 1px;
    height: 16px;
    margin: 0 2px;
    background: var(--border-d);
  }
</style>
