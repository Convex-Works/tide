<script lang="ts">
  import {
    ChatTeardropText,
    Microphone,
    MicrophoneSlash,
    PhoneDisconnect,
    Screencast,
    UsersThree,
    VideoCamera,
    VideoCameraSlash
  } from 'phosphor-svelte';
  import type { RoomState } from '$lib/rtc/room.svelte';

  let {
    rtc,
    onleave,
    peopleOpen = false,
    chatOpen = false,
    unreadChat = 0,
    ontogglepeople = () => undefined,
    ontogglechat = () => undefined
  }: {
    rtc: RoomState;
    onleave: () => void;
    peopleOpen?: boolean;
    chatOpen?: boolean;
    unreadChat?: number;
    ontogglepeople?: () => void;
    ontogglechat?: () => void;
  } = $props();

  async function leave(): Promise<void> {
    await rtc.leave().catch(() => undefined);
    onleave();
  }

  async function run(action: () => Promise<void>): Promise<void> {
    try {
      await action();
    } catch {
      // Browser media pickers may be cancelled without changing meeting rtc.
    }
  }
</script>

<nav class="control-bar" aria-label="Meeting controls">
  <button
    type="button"
    class:off={!rtc.micEnabled}
    aria-label={rtc.micEnabled ? 'Mute microphone' : 'Unmute microphone'}
    aria-pressed={rtc.micEnabled}
    title={rtc.micEnabled ? 'Mute microphone' : 'Unmute microphone'}
    onclick={() => void run(() => rtc.toggleMic())}
  >
    {#if rtc.micEnabled}
      <Microphone size={16} weight="regular" aria-hidden="true" />
    {:else}
      <MicrophoneSlash size={16} weight="regular" aria-hidden="true" />
    {/if}
  </button>

  <button
    type="button"
    class:off={!rtc.camEnabled}
    aria-label={rtc.camEnabled ? 'Turn camera off' : 'Turn camera on'}
    aria-pressed={rtc.camEnabled}
    title={rtc.camEnabled ? 'Turn camera off' : 'Turn camera on'}
    onclick={() => void run(() => rtc.toggleCam())}
  >
    {#if rtc.camEnabled}
      <VideoCamera size={16} weight="regular" aria-hidden="true" />
    {:else}
      <VideoCameraSlash size={16} weight="regular" aria-hidden="true" />
    {/if}
  </button>

  <button
    type="button"
    class:active={rtc.screenShareEnabled}
    aria-label={rtc.screenShareEnabled ? 'Stop sharing' : 'Share screen'}
    aria-pressed={rtc.screenShareEnabled}
    title={rtc.screenShareEnabled ? 'Stop sharing' : 'Share screen'}
    onclick={() => void run(() => rtc.toggleScreenShare())}
  >
    <Screencast size={16} weight="regular" aria-hidden="true" />
  </button>

  <button
    type="button"
    class="panel-control"
    class:active={peopleOpen}
    aria-label={peopleOpen ? 'Close people' : 'Open people'}
    aria-pressed={peopleOpen}
    title={peopleOpen ? 'Close people' : 'Open people'}
    onclick={ontogglepeople}
  >
    <UsersThree size={16} weight="regular" aria-hidden="true" />
    <span class="badge mono">{rtc.participants.length}</span>
  </button>

  <button
    type="button"
    class="panel-control"
    class:active={chatOpen}
    aria-label={chatOpen ? 'Close chat' : 'Open chat'}
    aria-pressed={chatOpen}
    title={chatOpen ? 'Close chat' : 'Open chat'}
    onclick={ontogglechat}
  >
    <ChatTeardropText size={16} weight="regular" aria-hidden="true" />
    {#if unreadChat > 0}<span class="badge unread mono">{unreadChat}</span>{/if}
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

  button.panel-control {
    position: relative;
  }

  .badge {
    position: absolute;
    top: -5px;
    right: -5px;
    min-width: 15px;
    height: 15px;
    padding: 0 3px;
    color: white;
    font-size: 9px;
    line-height: 13px;
    text-align: center;
    background: var(--accent-d);
    border: 1px solid var(--panel);
    border-radius: 999px;
  }

  .badge.unread {
    background: var(--rec);
  }

  .separator {
    width: 1px;
    height: 16px;
    margin: 0 2px;
    background: var(--border-d);
  }
</style>
