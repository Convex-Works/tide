<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte';
  import { Switch } from 'bits-ui';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import {
    ArrowClockwise,
    ArrowLeft,
    Check,
    Copy,
    DownloadSimple,
    Subtitles,
    Trash
  } from 'phosphor-svelte';
  import {
    ApiError,
    AuthRequiredError,
    deleteRecording,
    deleteRoom,
    errorMessage,
    listRecordings,
    listRooms,
    recordingDownloadURL,
    requestTranscript,
    transcriptDownloadURL,
    updateRoom
  } from '$lib/api/client';
  import {
    TranscriptAvailable,
    TranscriptCompleted,
    TranscriptFailed,
    TranscriptFormatText,
    TranscriptFormatVTT,
    TranscriptRunning,
    TranscriptWaiting,
    type RecordingInfo,
    type RoomInfo,
    type TranscriptInfo
  } from '$lib/api/types.gen';
  import { dateTimeLabel, durationLabel, isoDate, sizeLabel } from '$lib/format';
  import Button from '$lib/ui/Button.svelte';
  import RoomStatus from '$lib/ui/RoomStatus.svelte';

  type LoadState = 'loading' | 'signed-out' | 'not-found' | 'ready' | 'error';

  const pendingStatuses = ['starting', 'recording', 'finalizing'];

  // A running transcript reports progress, so it polls like a pending
  // recording. A waiting one can wait days for a machine to wake: poll it
  // slowly, and stop once nothing is in flight. A failed poll retries at the
  // slow pace too.
  const fastPollMs = 3_000;
  const slowPollMs = 15_000;

  // The small bordered controls and status pills of a recording's row.
  const rowControl =
    'inline-flex h-6 shrink-0 items-center gap-1 rounded-control border px-2 text-[11px] no-underline transition-colors disabled:opacity-60';
  const rowButton = `${rowControl} border-border bg-paper text-ink hover:bg-surface-2`;
  const pill = 'w-max shrink-0 rounded-full border px-1.5 text-[11px] leading-[18px]';

  // A completed recording is the normal case and carries no status; the
  // others say what the recording is doing, or that it failed.
  const recordingStatusWords: Record<string, string> = {
    starting: 'Starting',
    recording: 'Recording',
    finalizing: 'Finalizing',
    failed: 'Failed'
  };

  let loadState = $state<LoadState>('loading');
  let room = $state<RoomInfo>();
  let recordings = $state<RecordingInfo[]>([]);
  let recordingsLoading = $state(false);
  // Set only when the first load of the list failed; later polls fail quietly.
  let recordingsError = $state('');
  let error = $state('');
  let busy = $state(false);
  let copied = $state(false);
  let slugDraft = $state('');
  let slugSaved = $state(false);
  // Set once the typed link breaks the rules, so they show only then.
  let slugInvalid = $state(false);
  let deleteConfirm = $state(false);
  let deleteRecordingID = $state('');
  let transcriptBusyID = $state('');
  let copyTimer: ReturnType<typeof setTimeout> | undefined;
  let slugSavedTimer: ReturnType<typeof setTimeout> | undefined;
  let recordingPoll: ReturnType<typeof setTimeout> | undefined;
  // A poll came due while the tab was hidden; it runs when the tab is shown.
  let pollWhenVisible = false;
  // Bumped by every local change to the list, so a poll that was already in
  // flight can't put back a deleted recording or a stale transcript.
  let recordingsGeneration = 0;
  let destroyed = false;

  const slug = $derived(page.params.slug ?? '');
  // The transcript column only earns its width once a recording has one:
  // hosts without a machine never see transcripts (ARCHITECTURE.md §8.1).
  // Every transcript control hangs off `recording.transcript`, which a
  // server with transcripts off never sends, so they all go with it.
  const rowGrid = $derived(
    recordings.some((recording) => recording.transcript)
      ? 'grid-cols-[max-content_minmax(0,1fr)_auto] sm:grid-cols-[max-content_minmax(0,1fr)_56px_64px_auto] lg:grid-cols-[max-content_minmax(0,1fr)_56px_64px_248px_auto]'
      : 'grid-cols-[max-content_minmax(0,1fr)_auto] sm:grid-cols-[max-content_minmax(0,1fr)_56px_64px_auto]'
  );

  async function load(): Promise<void> {
    loadState = 'loading';
    error = '';
    try {
      const owned = await listRooms();
      const found = owned.find((item) => item.slug === slug);
      if (!found) {
        loadState = 'not-found';
        return;
      }
      room = found;
      slugDraft = found.slug;
      loadState = 'ready';
      await loadRecordings();
    } catch (cause) {
      if (cause instanceof AuthRequiredError) {
        loadState = 'signed-out';
        return;
      }
      error = errorMessage(
        cause,
        'Could not load the room. Check your connection, then try again.'
      );
      loadState = 'error';
    }
  }

  /** How soon to look at the list again, or undefined when nothing can change. */
  function pollDelay(list: RecordingInfo[]): number | undefined {
    const transcripts = list.map((recording) => recording.transcript?.status);
    if (
      list.some((recording) => pendingStatuses.includes(recording.status)) ||
      transcripts.includes(TranscriptRunning)
    ) {
      return fastPollMs;
    }
    if (transcripts.includes(TranscriptWaiting)) return slowPollMs;
    return undefined;
  }

  function schedulePoll(delay: number | undefined): void {
    clearTimeout(recordingPoll);
    recordingPoll = undefined;
    pollWhenVisible = false;
    if (delay == null || destroyed) return;
    recordingPoll = setTimeout(() => {
      recordingPoll = undefined;
      // A hidden tab doesn't poll; it catches up when it's shown again.
      if (document.hidden) pollWhenVisible = true;
      else void loadRecordings(true);
    }, delay);
  }

  function onVisibilityChange(): void {
    if (document.hidden || !pollWhenVisible) return;
    pollWhenVisible = false;
    void loadRecordings(true);
  }

  /**
   * Loads the list. Only the first load (background false) reports a
   * failure; a background poll keeps what's shown and tries again later.
   */
  async function loadRecordings(background = false): Promise<void> {
    if (destroyed || !room) return;
    const generation = recordingsGeneration;
    recordingsLoading = true;
    try {
      const list = await listRecordings(room.slug);
      if (destroyed || generation !== recordingsGeneration) return;
      recordings = list;
      recordingsError = '';
      schedulePoll(pollDelay(list));
    } catch (cause) {
      // A local change since the request started has already rescheduled.
      if (destroyed || generation !== recordingsGeneration) return;
      if (cause instanceof AuthRequiredError) {
        loadState = 'signed-out';
      } else if (background) {
        const delay = pollDelay(recordings);
        schedulePoll(delay == null ? undefined : Math.max(delay, slowPollMs));
      } else {
        recordingsError = errorMessage(
          cause,
          'Could not load the recordings. Check your connection, then try again.'
        );
      }
    } finally {
      recordingsLoading = false;
    }
  }

  async function copyLink(): Promise<void> {
    error = '';
    try {
      await navigator.clipboard.writeText(`${window.location.origin}/m/${room?.slug ?? slug}`);
      copied = true;
      if (copyTimer) clearTimeout(copyTimer);
      copyTimer = setTimeout(() => (copied = false), 1600);
    } catch {
      error = 'Could not copy the meeting link. Copy it from the address bar instead.';
    }
  }

  async function changeSlug(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!room || busy) return;
    const current = room;
    const next = slugDraft.trim().toLowerCase();
    if (next === current.slug) return;

    busy = true;
    slugSaved = false;
    error = '';
    try {
      const updated = await updateRoom(current.slug, { slug: next });
      room = { ...current, slug: updated.slug };
      slugDraft = updated.slug;
      slugInvalid = false;
      recordings = recordings.map((recording) => ({ ...recording, room_slug: updated.slug }));
      await goto(`/rooms/${updated.slug}`, { replaceState: true });
      slugSaved = true;
      if (slugSavedTimer) clearTimeout(slugSavedTimer);
      slugSavedTimer = setTimeout(() => (slugSaved = false), 1600);
    } catch (cause) {
      error = errorMessage(
        cause,
        'Could not change the room link. Check your connection, then try again.'
      );
    } finally {
      busy = false;
    }
  }

  async function toggleLobby(enabled: boolean): Promise<void> {
    if (!room || busy) return;
    const current = room;
    busy = true;
    error = '';
    try {
      const updated = await updateRoom(current.slug, { lobby_enabled: enabled });
      // The update path does not query the SFU, so preserve the live fields we
      // already have and only take the changed setting.
      room = { ...current, lobby_enabled: updated.lobby_enabled, name: updated.name };
    } catch (cause) {
      error = errorMessage(
        cause,
        'Could not change the lobby. Check your connection, then try again.'
      );
      // Re-assert the real state so the controlled switch snaps back.
      room = { ...current };
    } finally {
      busy = false;
    }
  }

  async function removeRoom(): Promise<void> {
    if (!room) return;
    if (!deleteConfirm) {
      deleteConfirm = true;
      return;
    }
    busy = true;
    error = '';
    try {
      await deleteRoom(room.slug);
      await goto('/');
    } catch (cause) {
      error = errorMessage(
        cause,
        'Could not delete the room. Check your connection, then try again.'
      );
      busy = false;
    }
  }

  async function removeRecording(id: string): Promise<void> {
    if (deleteRecordingID !== id) {
      deleteRecordingID = id;
      return;
    }
    busy = true;
    error = '';
    try {
      await deleteRecording(id);
      if (destroyed) return;
      recordingsGeneration += 1;
      recordings = recordings.filter((recording) => recording.id !== id);
      schedulePoll(pollDelay(recordings));
      deleteRecordingID = '';
      await focusOn(document.getElementById('recordings-heading'));
    } catch (cause) {
      if (cause instanceof AuthRequiredError) loadState = 'signed-out';
      else {
        error = errorMessage(
          cause,
          'Could not delete the recording. Check your connection, then try again.'
        );
      }
    } finally {
      busy = false;
    }
  }

  async function focusOn(element: HTMLElement | null): Promise<void> {
    await tick();
    element?.focus();
  }

  /** The transcript cell of a recording's row, which takes focus after its button goes. */
  function transcriptCellOf(id: string): HTMLElement | null {
    return document.querySelector(`[data-recording-id="${CSS.escape(id)}"] [data-transcript]`);
  }

  async function transcribe(id: string): Promise<void> {
    if (transcriptBusyID) return;
    transcriptBusyID = id;
    error = '';
    try {
      // For a transcript already pending, this answers with its status.
      const transcript = await requestTranscript(id);
      if (destroyed) return;
      recordingsGeneration += 1;
      recordings = recordings.map((recording) =>
        recording.id === id ? { ...recording, transcript } : recording
      );
      schedulePoll(pollDelay(recordings));
    } catch (cause) {
      if (destroyed) return;
      if (cause instanceof AuthRequiredError) {
        loadState = 'signed-out';
        return;
      }
      error = errorMessage(
        cause,
        'Could not request the transcript. Check your connection, then try again.'
      );
      // A conflict means the transcript changed since the list loaded
      // (requested elsewhere, already done, the owner's machines unpaired):
      // show what it is now.
      if (cause instanceof ApiError && cause.status === 409) {
        recordingsGeneration += 1;
        await loadRecordings(true);
      }
    } finally {
      transcriptBusyID = '';
    }
    await focusOn(transcriptCellOf(id));
  }

  function percent(progress?: number | null): string {
    if (progress == null) return '';
    return `${Math.round(Math.min(1, Math.max(0, progress)) * 100)}%`;
  }

  function statusTone(status: string): string {
    if (pendingStatuses.includes(status)) return 'text-warn border-warn/40 bg-warn/10';
    if (status === 'failed') return 'text-rec border-rec/30 bg-rec/10';
    return 'text-ink-2 border-border bg-surface';
  }

  function transcriptTone(status: string): string {
    if (status === TranscriptRunning) return 'border-warn/40 bg-warn/10 text-warn';
    if (status === TranscriptFailed) return 'border-rec/30 bg-rec/10 text-rec';
    return 'border-border text-ink-2';
  }

  /** The words the row's live region reads out: the status, never the progress. */
  function transcriptWords(transcript: TranscriptInfo | undefined, when: string): string {
    const words: Record<string, string> = {
      [TranscriptWaiting]: 'Transcript queued',
      [TranscriptRunning]: 'Transcribing',
      [TranscriptCompleted]: 'Transcript ready',
      [TranscriptFailed]: 'Transcript failed'
    };
    const status = transcript && words[transcript.status];
    return status ? `${status}: recording from ${when}` : '';
  }

  /** The line under the row: why a transcript waits, what a machine is doing, or why it failed. */
  function transcriptNote(transcript?: TranscriptInfo): string | undefined {
    if (transcript?.status === TranscriptFailed) return transcript.error;
    if (transcript?.status === TranscriptWaiting || transcript?.status === TranscriptRunning) {
      return transcript.message;
    }
    return undefined;
  }

  onMount(() => {
    void load();
    document.addEventListener('visibilitychange', onVisibilityChange);
  });
  onDestroy(() => {
    destroyed = true;
    if (copyTimer) clearTimeout(copyTimer);
    if (slugSavedTimer) clearTimeout(slugSavedTimer);
    clearTimeout(recordingPoll);
    document.removeEventListener('visibilitychange', onVisibilityChange);
  });
</script>

<svelte:head>
  <title>{room?.name ?? 'Room'} · klisi</title>
</svelte:head>

{#snippet transcriptCell(id: string, transcript: TranscriptInfo, when: string)}
  <span class="sr-only">Transcript:</span>
  {#if transcript.status === TranscriptAvailable}
    <button
      type="button"
      disabled={transcriptBusyID !== ''}
      class={rowButton}
      aria-label="Transcribe the recording from {when}"
      onclick={() => void transcribe(id)}
    >
      <Subtitles size={16} weight="regular" aria-hidden="true" /> Transcribe
    </button>
  {:else if transcript.status === TranscriptCompleted}
    {#if transcript.speakers != null}
      <span class="shrink-0 text-[11px] text-ink-2">
        {transcript.speakers}
        {transcript.speakers === 1 ? 'speaker' : 'speakers'}
      </span>
    {/if}
    <a
      class={rowButton}
      href={transcriptDownloadURL(id, TranscriptFormatText)}
      target="_blank"
      rel="noreferrer"
      aria-label="Download transcript of the recording from {when} (.txt)"
    >
      <DownloadSimple size={16} weight="regular" aria-hidden="true" /> Transcript
    </a>
    <!-- The same transcript as timed captions: for a video player or a
         podcast host, so a format beside the transcript, not a second one. -->
    <a
      class="mono shrink-0 text-[11px] text-ink-2 hover:text-accent"
      href={transcriptDownloadURL(id, TranscriptFormatVTT)}
      target="_blank"
      rel="noreferrer"
      aria-label="Download captions of the recording from {when} (.vtt)"
    >
      .vtt
    </a>
  {:else}
    <span class="{pill} {transcriptTone(transcript.status)}">
      {#if transcript.status === TranscriptRunning}
        Transcribing{#if transcript.progress != null}{' '}<span class="mono"
            >{percent(transcript.progress)}</span
          >{/if}
      {:else if transcript.status === TranscriptFailed}
        Transcript failed
      {:else}
        Transcript queued
      {/if}
    </span>
    {#if transcript.status === TranscriptFailed}
      <button
        type="button"
        disabled={transcriptBusyID !== ''}
        class={rowButton}
        aria-label="Retry the transcript of the recording from {when}"
        onclick={() => void transcribe(id)}
      >
        <ArrowClockwise size={16} weight="regular" aria-hidden="true" /> Retry
      </button>
    {/if}
  {/if}
{/snippet}

<header class="flex h-12 items-center justify-between border-b border-border px-4">
  <a class="text-[15px] font-[550] tracking-[0.02em] text-accent no-underline" href="/">klisi</a>
  <a
    class="inline-flex items-center gap-1 text-[12px] text-ink-2 no-underline transition-colors hover:text-ink"
    href="/"
  >
    <ArrowLeft size={16} weight="regular" aria-hidden="true" /> All rooms
  </a>
</header>

<main class="mx-auto w-[min(100%,880px)] px-4 py-8">
  {#if loadState === 'loading'}
    <p class="text-[12px] text-ink-2" aria-live="polite">Loading…</p>
  {:else if loadState === 'signed-out'}
    <p class="text-[13px] text-ink-2">
      Sign in from the <a class="text-accent" href="/">dashboard</a> to manage this room.
    </p>
  {:else if loadState === 'not-found'}
    <div class="rounded-card border border-border bg-surface px-4 py-8 text-center">
      <p class="text-[13px] text-ink">This room doesn't exist, or you don't own it.</p>
      <a class="mt-2 inline-block text-[12px] text-accent" href="/">← Back to rooms</a>
    </div>
  {:else if loadState === 'error'}
    <div class="rounded-card border border-border bg-surface px-4 py-8 text-center">
      <p class="text-[13px] text-rec" role="alert">{error}</p>
      <button class="mt-2 text-[12px] text-accent" type="button" onclick={() => void load()}>
        Try again
      </button>
    </div>
  {:else if room}
    <div class="min-w-0">
      <h1 class="m-0 truncate text-[18px] font-[550] leading-6 text-ink">{room.name}</h1>
      <div class="mt-1"><RoomStatus {room} /></div>
      <div class="mt-3 flex flex-wrap items-center gap-2">
        <Button href={`/m/${room.slug}`} variant="accent">Join meeting</Button>
        <Button type="button" variant="default" onclick={() => void copyLink()}>
          {#if copied}
            <Check size={16} weight="regular" aria-hidden="true" /> Copied
          {:else}
            <Copy size={16} weight="regular" aria-hidden="true" /> Copy link
          {/if}
        </Button>
      </div>
    </div>

    {#if error}<p class="mt-4 text-[12px] text-rec" role="alert">{error}</p>{/if}

    <section class="mt-8">
      <h2 id="recordings-heading" tabindex="-1" class="text-[13px] font-[550] text-ink">
        Recordings
      </h2>

      {#if recordingsLoading && recordings.length === 0}
        <p
          class="mt-2 rounded-card border border-border bg-surface px-3 py-6 text-center text-[12px] text-ink-2"
        >
          Loading recordings…
        </p>
      {:else if recordingsError && recordings.length === 0}
        <div class="mt-2 rounded-card border border-border bg-surface px-3 py-6 text-center">
          <p class="m-0 text-[12px] text-rec" role="alert">{recordingsError}</p>
          <button
            class="mt-2 border-0 bg-transparent p-0 text-[12px] text-accent"
            type="button"
            onclick={() => void loadRecordings()}
          >
            Try again
          </button>
        </div>
      {:else if recordings.length === 0}
        <p
          class="mt-2 rounded-card border border-border bg-surface px-3 py-6 text-center text-[12px] text-ink-2"
        >
          No recordings yet. They'll appear here after a meeting is recorded.
        </p>
      {:else}
        <div class="mt-2 overflow-hidden rounded-card border border-border bg-surface">
          {#each recordings as recording (recording.id)}
            {@const when = dateTimeLabel(recording.started_at)}
            {@const note = transcriptNote(recording.transcript)}
            <!-- DOM order is the narrow layout's reading order: the actions end
                 the first line and the transcript gets lines of its own. At lg
                 the actions move to the last column, and reading-flow keeps
                 Tab order visual where browsers support it. -->
            <div
              class="grid {rowGrid} items-center gap-x-3 gap-y-1.5 border-b border-border px-3 py-2.5 [reading-flow:grid-rows] last:border-b-0"
              data-recording-id={recording.id}
            >
              <span class="flex w-max items-center gap-1">
                {#if recording.status !== 'completed'}
                  <span class="{pill} {statusTone(recording.status)}">
                    {recordingStatusWords[recording.status] ?? recording.status}
                  </span>
                {/if}
                <span class="{pill} border-border text-ink-2">
                  {recording.audio_only ? 'Audio' : 'Video'}
                </span>
              </span>
              <time
                class="min-w-0 truncate text-[11px] text-ink-2"
                datetime={new Date(recording.started_at * 1000).toISOString()}
              >
                {isoDate(recording.started_at)}
              </time>
              <span class="hidden text-[11px] text-ink-2 sm:block">
                {durationLabel(recording.duration_s)}
              </span>
              <span class="hidden text-[11px] text-ink-2 sm:block">
                {sizeLabel(recording.size_bytes)}
              </span>
              <div class="flex justify-end gap-1 lg:col-[-2/-1] lg:row-start-1">
                {#if recording.status === 'completed'}
                  <a
                    class={rowButton}
                    href={recordingDownloadURL(recording.id)}
                    target="_blank"
                    rel="noreferrer"
                    aria-label="Download the recording from {when}"
                  >
                    <DownloadSimple size={16} weight="regular" aria-hidden="true" />
                    <span class="hidden sm:inline">Download</span>
                  </a>
                {/if}
                <button
                  type="button"
                  disabled={busy || pendingStatuses.includes(recording.status)}
                  class="{rowControl} {deleteRecordingID === recording.id
                    ? 'border-rec bg-rec text-white'
                    : 'border-border bg-paper text-ink-2 hover:bg-surface-2 hover:text-ink'}"
                  aria-label={deleteRecordingID === recording.id
                    ? `Confirm delete the recording from ${when}`
                    : `Delete the recording from ${when}`}
                  onclick={() => void removeRecording(recording.id)}
                >
                  {#if deleteRecordingID === recording.id}
                    Delete?
                  {:else}
                    <Trash size={16} weight="regular" aria-hidden="true" />
                  {/if}
                </button>
              </div>
              <!-- Its own column when there's room; its own line when there isn't.
                   The inner box takes focus when its button goes: a focusable
                   grid item would take its buttons out of the reading flow. -->
              <div
                class="col-span-full min-w-0 lg:col-span-1 {recording.transcript ? '' : 'hidden'}"
              >
                <div
                  class="flex w-fit max-w-full flex-wrap items-center gap-1.5 rounded-control"
                  tabindex="-1"
                  data-transcript
                  data-testid="transcript"
                  data-status={recording.transcript?.status}
                >
                  {#if recording.transcript}
                    {@render transcriptCell(recording.id, recording.transcript, when)}
                  {/if}
                </div>
              </div>
              {#if note}
                <p
                  class="col-span-full m-0 text-[11px] leading-4 text-ink [contain:inline-size] lg:col-[5/-1]"
                  data-testid="transcript-note"
                >
                  {note}
                </p>
              {/if}
              <span class="sr-only" aria-live="polite" data-testid="transcript-live">
                {transcriptWords(recording.transcript, when)}
              </span>
            </div>
          {/each}
        </div>
      {/if}
    </section>

    <section class="mt-8">
      <h2 class="text-[13px] font-[550] text-ink">Settings</h2>
      <div class="mt-2 overflow-hidden rounded-card border border-border bg-surface">
        <div class="flex items-center justify-between gap-4 px-3 py-2.5">
          <div class="min-w-0">
            <div class="text-[13px] text-ink">Lobby</div>
            <div class="text-[12px] text-ink-2">
              Hold guests in a waiting room until you admit them.
            </div>
          </div>
          <Switch.Root
            checked={room.lobby_enabled}
            onCheckedChange={(v) => void toggleLobby(v)}
            disabled={busy}
            aria-label="Lobby"
            class="relative box-border inline-block h-[18px] w-[32px] shrink-0 cursor-pointer appearance-none rounded-full border-0 p-0 transition-colors data-[state=checked]:bg-accent data-[state=unchecked]:bg-border disabled:opacity-60"
          >
            <Switch.Thumb
              class="pointer-events-none absolute left-[2px] top-[2px] block size-[14px] rounded-full bg-white transition-transform data-[state=checked]:translate-x-[14px] data-[state=unchecked]:translate-x-0"
            />
          </Switch.Root>
        </div>

        <form
          class="flex items-end justify-between gap-4 border-t border-border p-3"
          onsubmit={changeSlug}
        >
          <label class="min-w-0 flex-1" for="room-slug">
            <span class="block text-[13px] text-ink">Custom room link</span>
            <span class="mt-0.5 block text-[12px] text-ink-2">Changing it breaks the old link.</span
            >
            <input
              id="room-slug"
              name="room-slug"
              class="mono mt-2 h-7 w-full max-w-sm rounded-control border border-border bg-paper px-2 text-[12px] text-ink outline-none focus:border-accent aria-[invalid=true]:border-rec"
              value={slugDraft}
              minlength="3"
              maxlength="64"
              pattern="[a-z0-9]+(-[a-z0-9]+)*"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              disabled={busy || room.active || room.recording}
              aria-describedby="room-slug-status"
              aria-invalid={slugInvalid}
              oninput={(event) => {
                slugDraft = event.currentTarget.value.toLowerCase();
                // Once flagged, the rules stay until the link follows them.
                if (slugInvalid) slugInvalid = !event.currentTarget.validity.valid;
              }}
              onblur={(event) => (slugInvalid = !event.currentTarget.validity.valid)}
              oninvalid={(event) => {
                // Say it here, in the row, rather than in the browser's bubble.
                event.preventDefault();
                slugInvalid = true;
              }}
              required
            />
            <span
              id="room-slug-status"
              class="mt-1 block text-[11px] {slugInvalid && !room.active && !room.recording
                ? 'text-rec'
                : 'text-ink-2'}"
            >
              {#if room.active || room.recording}
                The link can be changed after the meeting and recording stop.
              {:else if slugInvalid}
                Use 3–64 lowercase letters, numbers, and single hyphens.
              {:else}
                Meeting URL: /m/{slugDraft || room.slug}
              {/if}
            </span>
          </label>
          <button
            type="submit"
            disabled={busy ||
              room.active ||
              room.recording ||
              slugDraft.trim().toLowerCase() === room.slug}
            class="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-control border border-border bg-paper px-2.5 text-[12px] text-ink transition-colors hover:bg-surface-2 disabled:opacity-60"
          >
            {#if slugSaved}
              <Check size={16} weight="regular" aria-hidden="true" /> Saved
            {:else}
              Save link
            {/if}
          </button>
        </form>

        <div class="flex items-center justify-between gap-4 border-t border-border p-3">
          <div class="min-w-0">
            <div class="text-[13px] text-ink">Delete room</div>
            <div class="text-[12px] text-ink-2">
              Removes the room, its link, and every recording. This can't be undone.
            </div>
          </div>
          <button
            type="button"
            disabled={busy}
            onclick={() => void removeRoom()}
            class="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-control border px-2.5 text-[12px] transition-colors disabled:opacity-60 {deleteConfirm
              ? 'border-rec bg-rec text-white'
              : 'border-border bg-paper text-ink-2 hover:bg-surface-2 hover:text-ink'}"
          >
            {#if deleteConfirm}
              Confirm delete
            {:else}
              <Trash size={16} weight="regular" aria-hidden="true" /> Delete
            {/if}
          </button>
        </div>
      </div>
    </section>
  {/if}
</main>
