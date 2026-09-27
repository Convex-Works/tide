<script lang="ts">
  import { onDestroy, onMount, type Snippet } from 'svelte';
  import { Microphone, MicrophoneSlash, VideoCamera, VideoCameraSlash } from 'phosphor-svelte';
  import {
    Room,
    createLocalAudioTrack,
    createLocalVideoTrack,
    type LocalAudioTrack,
    type LocalVideoTrack
  } from 'livekit-client';
  import type { PreJoinOptions } from './room.svelte';
  import { clampAspect, observeAspect } from './aspect';
  import { attachMediaTrack } from './mediaElement';
  import Button from '$lib/ui/Button.svelte';

  let {
    room = $bindable(),
    name = $bindable(),
    error = '',
    showRoom = true,
    heading = 'Join a room',
    onactivateplayback = () => undefined,
    onjoin,
    account
  }: {
    room: string;
    name: string;
    error?: string;
    showRoom?: boolean;
    heading?: string;
    onactivateplayback?: () => void;
    onjoin: (options: PreJoinOptions) => void | Promise<void>;
    account?: Snippet;
  } = $props();

  let cameras = $state<MediaDeviceInfo[]>([]);
  let microphones = $state<MediaDeviceInfo[]>([]);
  let videoDeviceId = $state('');
  let audioDeviceId = $state('');
  let camEnabled = $state(true);
  let micEnabled = $state(true);
  let cameraTrack = $state<LocalVideoTrack>();
  let previewAspect = $state(clampAspect(0, 0));
  let audioTrack: LocalAudioTrack | undefined;
  let micLevel = $state(0);
  let previewBusy = $state(true);
  let previewError = $state('');

  let audioContext: AudioContext | undefined;
  let analyser: AnalyserNode | undefined;
  let audioSource: MediaStreamAudioSourceNode | undefined;
  let meterFrame = 0;
  let cameraRequest = 0;
  let audioRequest = 0;
  let disposed = false;

  async function refreshDevices(requestPermissions = false): Promise<void> {
    const devices = await Room.getLocalDevices(undefined, requestPermissions);
    cameras = devices.filter((device) => device.kind === 'videoinput');
    microphones = devices.filter((device) => device.kind === 'audioinput');

    if (!cameras.some((device) => device.deviceId === videoDeviceId)) {
      videoDeviceId = cameras[0]?.deviceId ?? '';
    }
    if (!microphones.some((device) => device.deviceId === audioDeviceId)) {
      audioDeviceId = microphones[0]?.deviceId ?? '';
    }
  }

  function stopCamera(invalidate = true): void {
    if (invalidate) cameraRequest += 1;
    cameraTrack?.detach();
    cameraTrack?.stop();
    cameraTrack = undefined;
    previewAspect = clampAspect(0, 0);
  }

  async function startCamera(): Promise<void> {
    const request = ++cameraRequest;
    stopCamera(false);
    if (!camEnabled || disposed) return;

    try {
      const track = await createLocalVideoTrack(
        videoDeviceId ? { deviceId: { exact: videoDeviceId } } : undefined
      );
      if (disposed || request !== cameraRequest || !camEnabled) {
        track.stop();
        return;
      }
      cameraTrack = track;
      previewError = '';
    } catch {
      if (request === cameraRequest) {
        camEnabled = false;
        previewError = 'Camera unavailable.';
      }
    }
  }

  function stopMeter(): void {
    audioRequest += 1;
    cancelAnimationFrame(meterFrame);
    meterFrame = 0;
    audioSource?.disconnect();
    analyser?.disconnect();
    audioSource = undefined;
    analyser = undefined;
    audioTrack?.stop();
    audioTrack = undefined;
    micLevel = 0;
    if (audioContext) {
      void audioContext.close().catch(() => undefined);
      audioContext = undefined;
    }
  }

  function sampleMicLevel(): void {
    if (!analyser) return;
    const samples = new Uint8Array(analyser.fftSize);
    analyser.getByteTimeDomainData(samples);
    let sum = 0;
    for (const sample of samples) {
      const normalized = (sample - 128) / 128;
      sum += normalized * normalized;
    }
    const rms = Math.sqrt(sum / samples.length);
    micLevel = Math.min(1, micLevel * 0.72 + rms * 4 * 0.28);
    meterFrame = requestAnimationFrame(sampleMicLevel);
  }

  async function startMicrophone(): Promise<void> {
    stopMeter();
    const request = ++audioRequest;
    if (!micEnabled || disposed) return;

    try {
      const track = await createLocalAudioTrack(
        audioDeviceId ? { deviceId: { exact: audioDeviceId } } : undefined
      );
      if (disposed || request !== audioRequest || !micEnabled) {
        track.stop();
        return;
      }

      const context = new AudioContext();
      const nextAnalyser = context.createAnalyser();
      nextAnalyser.fftSize = 256;
      nextAnalyser.smoothingTimeConstant = 0.7;
      const source = context.createMediaStreamSource(new MediaStream([track.mediaStreamTrack]));
      source.connect(nextAnalyser);

      audioTrack = track;
      audioContext = context;
      analyser = nextAnalyser;
      audioSource = source;
      sampleMicLevel();
    } catch {
      if (request === audioRequest) micEnabled = false;
    }
  }

  async function initialize(): Promise<void> {
    previewBusy = true;
    try {
      await refreshDevices(true);
      await Promise.all([startCamera(), startMicrophone()]);
      await refreshDevices(false);
    } catch {
      previewError = 'Check camera and microphone access.';
      await Promise.all([startCamera(), startMicrophone()]);
    } finally {
      previewBusy = false;
    }
  }

  async function toggleCamera(): Promise<void> {
    camEnabled = !camEnabled;
    if (camEnabled) await startCamera();
    else stopCamera();
  }

  async function toggleMicrophone(): Promise<void> {
    micEnabled = !micEnabled;
    if (micEnabled) await startMicrophone();
    else stopMeter();
  }

  async function changeCamera(event: Event): Promise<void> {
    videoDeviceId = (event.currentTarget as HTMLSelectElement).value;
    if (camEnabled) await startCamera();
  }

  async function changeMicrophone(event: Event): Promise<void> {
    audioDeviceId = (event.currentTarget as HTMLSelectElement).value;
    if (micEnabled) await startMicrophone();
  }

  async function disposePreview(): Promise<void> {
    disposed = true;
    stopCamera();
    stopMeter();
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    room = room.trim();
    name = name.trim();
    if (!room || !name) return;

    // LiveKit's playback unlock must begin in the Join click stack, before
    // token fetches, lobby waits, or the WebSocket connection consume it.
    onactivateplayback();
    await disposePreview();
    await onjoin({ name, micEnabled, camEnabled, videoDeviceId, audioDeviceId });
  }

  onMount(() => {
    void initialize();
  });

  onDestroy(() => {
    void disposePreview();
  });
</script>

<main class="prejoin-shell">
  <form class="prejoin-card" onsubmit={submit}>
    <div class="preview-well" class:live={cameraTrack && camEnabled}>
      {#if cameraTrack && camEnabled}
        <!-- The preview box takes the broadcast frame's clamped aspect, so
             what you see here is exactly what remote tiles render. -->
        <div class="video-box" style:--va={previewAspect}>
          <!-- Local preview video does not have a caption track. -->
          <video
            use:attachMediaTrack={cameraTrack}
            use:observeAspect={(next) => (previewAspect = next)}
            autoplay
            playsinline
            muted
            aria-label="Camera preview"
          ></video>
        </div>
      {:else}
        <div class="preview-placeholder" aria-label="Camera off">
          <VideoCameraSlash size={20} weight="regular" aria-hidden="true" />
        </div>
      {/if}

      <button
        class:off={!camEnabled}
        class="preview-toggle"
        type="button"
        aria-label={camEnabled ? 'Turn camera off' : 'Turn camera on'}
        aria-pressed={camEnabled}
        title={camEnabled ? 'Turn camera off' : 'Turn camera on'}
        onclick={() => void toggleCamera()}
      >
        {#if camEnabled}
          <VideoCamera size={20} weight="regular" aria-hidden="true" />
        {:else}
          <VideoCameraSlash size={20} weight="regular" aria-hidden="true" />
        {/if}
      </button>
    </div>

    <div class="details">
      <div class="brand-row">
        <div class="brand">klisi</div>
        {@render account?.()}
      </div>
      <h1 class:room-heading={!showRoom}>{heading}</h1>

      {#if showRoom}
        <label>
          <span>Room</span>
          <input class="mono" bind:value={room} name="room" autocomplete="off" required />
        </label>
      {/if}

      <label>
        <span>Name</span>
        <input bind:value={name} name="name" autocomplete="name" required />
      </label>

      <div class="device-row">
        <label>
          <span>Camera</span>
          <select
            bind:value={videoDeviceId}
            onchange={changeCamera}
            disabled={cameras.length === 0}
          >
            {#each cameras as device, index (device.deviceId)}
              <option value={device.deviceId}>{device.label || `Camera ${index + 1}`}</option>
            {/each}
          </select>
        </label>

        <label>
          <span>Microphone</span>
          <select
            bind:value={audioDeviceId}
            onchange={changeMicrophone}
            disabled={microphones.length === 0}
          >
            {#each microphones as device, index (device.deviceId)}
              <option value={device.deviceId}>{device.label || `Microphone ${index + 1}`}</option>
            {/each}
          </select>
        </label>
      </div>

      <div class="mic-control">
        <button
          class:off={!micEnabled}
          class="mic-toggle"
          type="button"
          aria-label={micEnabled ? 'Mute microphone' : 'Unmute microphone'}
          aria-pressed={micEnabled}
          title={micEnabled ? 'Mute microphone' : 'Unmute microphone'}
          onclick={() => void toggleMicrophone()}
        >
          {#if micEnabled}
            <Microphone size={20} weight="regular" aria-hidden="true" />
          {:else}
            <MicrophoneSlash size={20} weight="regular" aria-hidden="true" />
          {/if}
        </button>
        <div
          class="meter"
          role="meter"
          aria-label="Microphone level"
          aria-valuemin="0"
          aria-valuemax="100"
          aria-valuenow={Math.round(micLevel * 100)}
        >
          <span style:width={`${Math.round(micLevel * 100)}%`}></span>
        </div>
      </div>

      {#if error || previewError}
        <p class="error" role="alert">{error || previewError}</p>
      {/if}

      <Button type="submit" variant="accent" class="mt-3 w-full" disabled={previewBusy}>
        <VideoCamera size={16} weight="regular" aria-hidden="true" />
        {previewBusy ? 'Preparing…' : 'Join room'}
      </Button>
    </div>
  </form>
</main>

<style>
  .prejoin-shell {
    display: grid;
    min-height: 100dvh;
    padding: 24px 12px;
    place-items: center;
    background: var(--paper);
  }

  .prejoin-card {
    display: grid;
    width: min(100%, 720px);
    grid-template-columns: minmax(0, 1.2fr) minmax(280px, 0.8fr);
    overflow: hidden;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-card);
  }

  .preview-well {
    position: relative;
    display: grid;
    min-height: 360px;
    place-items: center;
    overflow: hidden;
    background: var(--surface-2);
    border-right: 1px solid var(--border);
    /* Size containment gives the video box exact cqh units to contain-fit
       its clamped aspect (same mechanism as in-room tiles). */
    container-type: size;
  }

  .video-box {
    overflow: hidden;
    aspect-ratio: var(--va);
    width: clamp(min(140px, 100%), calc(100cqh * var(--va)), 100%);
    max-height: 100%;
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-tile);
    transition:
      width var(--motion-fast),
      aspect-ratio var(--motion-fast);
  }

  video,
  .preview-placeholder {
    width: 100%;
    height: 100%;
  }

  video {
    object-fit: cover;
    transform: scaleX(-1);
  }

  @media (prefers-reduced-motion: reduce) {
    .video-box {
      transition: none;
    }
  }

  .preview-placeholder {
    display: grid;
    place-items: center;
    color: var(--text-2);
    background: var(--panel-2);
  }

  .details {
    padding: 12px;
  }

  .brand-row {
    display: flex;
    min-height: var(--control-height);
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 8px;
  }

  .brand {
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

  h1.room-heading {
    margin-bottom: 8px;
    font-size: 24px;
    line-height: 30px;
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

  input,
  select {
    width: 100%;
    height: var(--control-height);
    min-width: 0;
    padding: 3px 7px;
    color: var(--ink);
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
    transition: border-color var(--motion-fast);
  }

  select {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  input:hover,
  select:hover:not(:disabled) {
    border-color: var(--ink-2);
  }

  select:disabled {
    color: var(--ink-2);
  }

  .device-row {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }

  .device-row label {
    min-width: 0;
  }

  .preview-toggle,
  .mic-toggle {
    display: grid;
    width: var(--control-height);
    height: var(--control-height);
    padding: 0;
    place-items: center;
    color: var(--ink);
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  .preview-toggle {
    position: absolute;
    right: 8px;
    bottom: 8px;
  }

  .preview-toggle.off,
  .mic-toggle.off {
    color: var(--rec);
  }

  .mic-control {
    display: grid;
    grid-template-columns: var(--control-height) minmax(0, 1fr);
    align-items: center;
    gap: 8px;
  }

  .meter {
    height: 3px;
    flex: 1;
    overflow: hidden;
    background: var(--border);
    border-radius: 999px;
  }

  .meter span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 60ms linear;
  }

  .error {
    margin: 8px 0 0;
    color: var(--rec);
    font-size: 12px;
  }

  @media (max-width: 680px) {
    .prejoin-card {
      grid-template-columns: 1fr;
    }

    .preview-well {
      min-height: auto;
      border-right: 0;
      border-bottom: 1px solid var(--border);
      container-type: normal;
    }

    /* Camera off: keep a landscape well for the placeholder. Live video is
       width-driven at its own clamped aspect (portrait phones stay portrait). */
    .preview-well:not(.live) {
      aspect-ratio: 16 / 9;
    }

    .video-box {
      width: 100%;
      max-height: 65dvh;
    }
  }
</style>
