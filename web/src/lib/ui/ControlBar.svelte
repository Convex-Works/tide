<script lang="ts">
  import {
    ChatTeardropText,
    Microphone,
    MicrophoneSlash,
    PhoneDisconnect,
    Record,
    Screencast,
    UsersThree,
    VideoCamera,
    VideoCameraSlash
  } from 'phosphor-svelte';
  import type { RoomState } from '$lib/rtc/room.svelte';
  import { startRecording, stopRecording } from '$lib/api/client';

  let {
    rtc,
    onleave,
    peopleOpen = false,
    chatOpen = false,
    unreadChat = 0,
    isOwner = false,
    roomSlug = '',
    ontogglepeople = () => undefined,
    ontogglechat = () => undefined
  }: {
    rtc: RoomState;
    onleave: () => void;
    peopleOpen?: boolean;
    chatOpen?: boolean;
    unreadChat?: number;
    isOwner?: boolean;
    roomSlug?: string;
    ontogglepeople?: () => void;
    ontogglechat?: () => void;
  } = $props();

  let recordingConfirm = $state(false);
  let recordingBusy = $state(false);
  let recordingError = $state('');

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

  async function toggleRecording(): Promise<void> {
    if (recordingBusy || !roomSlug) return;
    if (!rtc.isRecording && !recordingConfirm) {
      recordingConfirm = true;
      return;
    }
    recordingBusy = true;
    recordingConfirm = false;
    recordingError = '';
    try {
      if (rtc.isRecording) {
        await stopRecording(roomSlug);
      } else {
        await startRecording(roomSlug);
      }
    } catch (cause) {
      recordingError = cause instanceof Error ? cause.message : 'Could not change recording.';
    } finally {
      recordingBusy = false;
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

  {#if isOwner}
    <button
      type="button"
      class="record-control"
      class:recording={rtc.isRecording}
      class:confirm={recordingConfirm}
      disabled={recordingBusy}
      aria-label={rtc.isRecording
        ? 'Stop recording'
        : recordingConfirm
          ? 'Record?'
          : 'Start recording'}
      aria-pressed={rtc.isRecording}
      title={rtc.isRecording ? 'Stop recording' : 'Start recording'}
      onclick={() => void toggleRecording()}
    >
      <Record size={16} weight="regular" aria-hidden="true" />
      {#if recordingConfirm}<span>Record?</span>{/if}
    </button>
  {/if}

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

  {#if recordingError}<span class="recording-error" role="alert">{recordingError}</span>{/if}

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
    gap: 8px;
    padding: 12px;
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
    transform: translateX(-50%);
  }

  button {
    display: grid;
    width: var(--control-height);
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

  button.record-control.recording {
    color: white;
    background: var(--rec);
    border-color: var(--rec);
  }

  button.record-control.confirm {
    display: flex;
    width: auto;
    gap: 4px;
    padding: 0 7px;
    color: var(--rec);
    border-color: color-mix(in srgb, var(--rec) 42%, transparent);
  }

  button:disabled {
    cursor: wait;
    opacity: 0.6;
  }

  .recording-error {
    position: absolute;
    right: 0;
    bottom: calc(100% + 6px);
    width: max-content;
    max-width: 280px;
    padding: 4px 7px;
    color: var(--text);
    font-size: 11px;
    background: var(--panel);
    border: 1px solid var(--rec);
    border-radius: var(--radius-control);
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
