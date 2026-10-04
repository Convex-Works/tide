<script lang="ts">
  import {
    CaretUp,
    ChatTeardropText,
    Check,
    GridFour,
    Microphone,
    MicrophoneSlash,
    PhoneDisconnect,
    Power,
    Record,
    Screencast,
    SignOut,
    UserRectangle,
    UsersThree,
    VideoCamera,
    VideoCameraSlash
  } from 'phosphor-svelte';
  import type { RoomState } from '$lib/rtc/room.svelte';
  import { endMeeting, startRecording, stopRecording } from '$lib/api/client';

  let {
    rtc,
    onleave,
    peopleOpen = false,
    chatOpen = false,
    unreadChat = 0,
    canManage = false,
    roomSlug = '',
    view = 'grid',
    ontogglepeople = () => undefined,
    ontogglechat = () => undefined,
    ontoggleview = () => undefined
  }: {
    rtc: RoomState;
    onleave: () => void;
    peopleOpen?: boolean;
    chatOpen?: boolean;
    unreadChat?: number;
    canManage?: boolean;
    roomSlug?: string;
    view?: 'grid' | 'speaker';
    ontogglepeople?: () => void;
    ontogglechat?: () => void;
    ontoggleview?: () => void;
  } = $props();

  type DeviceMenuKind = 'audioinput' | 'videoinput';

  let recordingConfirm = $state(false);
  let recordingBusy = $state(false);
  let recordingError = $state('');
  let recordVideo = $state(false);
  let leaveConfirm = $state(false);
  let endBusy = $state(false);
  let endError = $state('');
  let deviceMenu = $state<DeviceMenuKind | ''>('');

  async function toggleDeviceMenu(kind: DeviceMenuKind): Promise<void> {
    if (deviceMenu === kind) {
      deviceMenu = '';
      return;
    }
    await rtc.refreshDevices().catch(() => undefined);
    // On a phone every menu and confirm step opens in the same place above
    // the bar, so opening one closes the others.
    recordingConfirm = false;
    leaveConfirm = false;
    deviceMenu = kind;
  }

  function toggleLeave(): void {
    leaveConfirm = !leaveConfirm;
    if (leaveConfirm) {
      deviceMenu = '';
      recordingConfirm = false;
    }
  }

  function pickDevice(kind: DeviceMenuKind, deviceId: string): void {
    deviceMenu = '';
    void run(() => rtc.switchDevice(kind, deviceId));
  }

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
      recordVideo = false;
      recordingError = '';
      deviceMenu = '';
      leaveConfirm = false;
      return;
    }
    recordingBusy = true;
    recordingConfirm = false;
    recordingError = '';
    try {
      if (rtc.isRecording) {
        await stopRecording(roomSlug);
      } else {
        await startRecording(roomSlug, { video: recordVideo });
      }
    } catch (cause) {
      recordingError = cause instanceof Error ? cause.message : 'Could not change recording.';
    } finally {
      recordingBusy = false;
    }
  }

  async function endForAll(): Promise<void> {
    if (endBusy || !roomSlug) return;
    endBusy = true;
    endError = '';
    try {
      await endMeeting(roomSlug);
      leaveConfirm = false;
      // No local teardown: the server disconnects this client too, and the
      // meeting page renders the shared "meeting ended" state for everyone.
    } catch (cause) {
      endError = cause instanceof Error ? cause.message : 'Could not end the meeting.';
    } finally {
      endBusy = false;
    }
  }
</script>

{#snippet deviceMenuList(kind: DeviceMenuKind, fallbackLabel: string)}
  <!-- The stopPropagation shield keeps the window click-away handler from
       closing the menu; keyboard interaction lives on the options. -->
  <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events, a11y_interactive_supports_focus -->
  <div
    class="device-menu"
    role="menu"
    aria-label={`Choose ${fallbackLabel.toLowerCase()}`}
    onclick={(event) => event.stopPropagation()}
  >
    {#each rtc.devices[kind] as device, index (device.deviceId)}
      <button
        type="button"
        class="device-option"
        role="menuitemradio"
        aria-checked={rtc.activeDeviceIds[kind] === device.deviceId}
        onclick={() => pickDevice(kind, device.deviceId)}
      >
        <span class="device-name">{device.label || `${fallbackLabel} ${index + 1}`}</span>
        {#if rtc.activeDeviceIds[kind] === device.deviceId}
          <Check size={12} weight="bold" aria-hidden="true" />
        {/if}
      </button>
    {:else}
      <span class="device-empty">No devices found</span>
    {/each}
  </div>
{/snippet}

<svelte:window
  onclick={() => {
    deviceMenu = '';
    recordingConfirm = false;
    leaveConfirm = false;
  }}
  onkeydown={(event) => {
    if (event.key === 'Escape') {
      deviceMenu = '';
      recordingConfirm = false;
      leaveConfirm = false;
    }
  }}
/>

<nav class="control-bar" aria-label="Meeting controls">
  <div class="control-group">
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
      class="caret"
      class:active={deviceMenu === 'audioinput'}
      aria-label="Choose microphone"
      aria-haspopup="menu"
      aria-expanded={deviceMenu === 'audioinput'}
      title="Choose microphone"
      onclick={(event) => {
        event.stopPropagation();
        void toggleDeviceMenu('audioinput');
      }}
    >
      <CaretUp size={10} weight="bold" aria-hidden="true" />
    </button>
    {#if deviceMenu === 'audioinput'}
      {@render deviceMenuList('audioinput', 'Microphone')}
    {/if}
  </div>

  <div class="control-group">
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
      class="caret"
      class:active={deviceMenu === 'videoinput'}
      aria-label="Choose camera"
      aria-haspopup="menu"
      aria-expanded={deviceMenu === 'videoinput'}
      title="Choose camera"
      onclick={(event) => {
        event.stopPropagation();
        void toggleDeviceMenu('videoinput');
      }}
    >
      <CaretUp size={10} weight="bold" aria-hidden="true" />
    </button>
    {#if deviceMenu === 'videoinput'}
      {@render deviceMenuList('videoinput', 'Camera')}
    {/if}
  </div>

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

  {#if canManage}
    <!-- The stopPropagation shield keeps the window click-away handler from
         collapsing the confirm state; interaction lives on the controls. -->
    <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
    <div class="record-group" onclick={(event) => event.stopPropagation()}>
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
      {#if recordingConfirm}
        <div class="record-confirm">
          <!-- On a phone the bar's button has no room for "Record?", so the
               choice opens above the bar with its own confirm. -->
          <button
            type="button"
            class="record-now"
            disabled={recordingBusy}
            onclick={() => void toggleRecording()}
          >
            <Record size={16} weight="regular" aria-hidden="true" />
            <span>Record</span>
          </button>
          <label class="record-video">
            <input type="checkbox" bind:checked={recordVideo} />
            Also record video
          </label>
        </div>
      {/if}
    </div>
  {/if}

  <span class="separator" aria-hidden="true"></span>

  <button
    type="button"
    aria-label={view === 'grid' ? 'Speaker view' : 'Grid view'}
    aria-pressed={view === 'speaker'}
    title={view === 'grid' ? 'Speaker view' : 'Grid view'}
    onclick={ontoggleview}
  >
    {#if view === 'grid'}
      <UserRectangle size={16} weight="regular" aria-hidden="true" />
    {:else}
      <GridFour size={16} weight="regular" aria-hidden="true" />
    {/if}
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

  {#if recordingError || endError}
    <span class="recording-error" class:raised={leaveConfirm || recordingConfirm} role="alert"
      >{recordingError || endError}</span
    >
  {/if}

  {#if canManage}
    <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
    <div
      class="leave-group"
      class:confirming={leaveConfirm}
      onclick={(event) => event.stopPropagation()}
    >
      <!-- In place of the choices on a wide bar; it stays as their anchor on
           a phone, where the choices open above the bar. -->
      <button
        type="button"
        class="leave toggle"
        aria-label="Leave or end meeting"
        aria-expanded={leaveConfirm}
        title="Leave"
        onclick={toggleLeave}
      >
        <PhoneDisconnect size={16} weight="regular" aria-hidden="true" />
      </button>
      {#if leaveConfirm}
        <div class="leave-choices">
          <button
            type="button"
            class="leave expanded"
            aria-label="Leave room"
            title="Leave room"
            onclick={() => void leave()}
          >
            <SignOut size={16} weight="regular" aria-hidden="true" />
            <span>Leave</span>
          </button>
          <button
            type="button"
            class="end-all"
            disabled={endBusy}
            aria-label="End meeting for all"
            title="End meeting for all"
            onclick={() => void endForAll()}
          >
            <Power size={16} weight="regular" aria-hidden="true" />
            <span>End for all</span>
          </button>
        </div>
      {/if}
    </div>
  {:else}
    <button
      type="button"
      class="leave"
      aria-label="Leave room"
      title="Leave room"
      onclick={() => void leave()}
    >
      <PhoneDisconnect size={16} weight="regular" aria-hidden="true" />
    </button>
  {/if}
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

  .record-group,
  .record-confirm,
  .leave-group,
  .leave-choices {
    display: flex;
    gap: 4px;
    align-items: center;
  }

  .leave-group.confirming > button.toggle {
    display: none;
  }

  .record-video {
    display: flex;
    gap: 4px;
    align-items: center;
    color: var(--text-2);
    font-size: 12px;
    white-space: nowrap;
  }

  .record-video input {
    margin: 0;
    accent-color: var(--accent-d);
  }

  button.record-now {
    display: none;
  }

  button.leave.expanded,
  button.end-all {
    display: flex;
    width: auto;
    gap: 4px;
    padding: 0 7px;
    font-size: 12px;
    white-space: nowrap;
  }

  button.end-all {
    color: white;
    background: var(--rec);
    border-color: var(--rec);
  }

  button.end-all:hover {
    background: color-mix(in srgb, var(--rec) 85%, black);
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

  .control-group {
    position: relative;
    display: flex;
    gap: 1px;
    align-items: center;
  }

  button.caret {
    width: 15px;
    color: var(--text);
    opacity: 0.75;
  }

  button.caret[aria-expanded='true'] {
    opacity: 1;
  }

  .device-menu {
    position: absolute;
    bottom: calc(100% + 14px);
    left: 50%;
    display: grid;
    gap: 1px;
    min-width: 200px;
    max-width: 280px;
    padding: 3px;
    background: var(--panel);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-card);
    transform: translateX(-50%);
  }

  .device-menu button.device-option {
    display: flex;
    width: 100%;
    height: auto;
    gap: 10px;
    align-items: center;
    justify-content: space-between;
    padding: 5px 8px;
    font-size: 12px;
    text-align: left;
  }

  .device-name {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .device-empty {
    padding: 5px 8px;
    font-size: 12px;
    opacity: 0.6;
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

  /* A host's full bar is 364px wide. On a phone it drops the separators and
     tightens to 298px, leaving slack inside 320px; the confirm steps and
     device menus open above the bar, anchored to its edges, instead of
     widening it. Their popovers have the bar as containing block. */
  @media (max-width: 400px) {
    .control-bar {
      gap: 4px;
      padding: 6px;
    }

    .separator {
      display: none;
    }

    .control-group {
      position: static;
    }

    .device-menu {
      bottom: calc(100% + 6px);
      left: 0;
      max-width: calc(100vw - 24px);
      transform: none;
    }

    button.record-control.confirm {
      display: grid;
      width: var(--control-height);
      padding: 0;
    }

    button.record-control.confirm span {
      display: none;
    }

    .record-confirm,
    .leave-choices {
      position: absolute;
      bottom: calc(100% + 6px);
      gap: 8px;
      padding: 6px;
      background: var(--panel);
      border: 1px solid var(--border-d);
      border-radius: var(--radius-card);
    }

    .record-confirm {
      left: 0;
      padding-right: 8px;
    }

    .leave-choices {
      right: 0;
      gap: 4px;
    }

    button.record-now {
      display: flex;
      width: auto;
      gap: 4px;
      padding: 0 7px;
      color: white;
      font-size: 12px;
      white-space: nowrap;
      background: var(--rec);
      border-color: var(--rec);
    }

    button.record-now:hover {
      background: color-mix(in srgb, var(--rec) 85%, black);
    }

    .record-video {
      color: var(--text);
    }

    .leave-group.confirming > button.toggle {
      display: grid;
      background: color-mix(in srgb, var(--rec) 12%, transparent);
      border-color: color-mix(in srgb, var(--rec) 32%, transparent);
    }

    .recording-error.raised {
      bottom: calc(100% + 54px);
    }
  }
</style>
