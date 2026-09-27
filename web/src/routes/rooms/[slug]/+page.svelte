<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { Switch } from 'bits-ui';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import {
    ArrowClockwise,
    ArrowLeft,
    ArrowRight,
    Check,
    Copy,
    DownloadSimple,
    Subtitles,
    Trash
  } from 'phosphor-svelte';
  import {
    AuthRequiredError,
    deleteRecording,
    deleteRoom,
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
  import { compactAgo, durationLabel, relativeDate, sizeLabel } from '$lib/format';
  import StateTile from '$lib/ui/StateTile.svelte';
  import Button from '$lib/ui/Button.svelte';

  type LoadState = 'loading' | 'signed-out' | 'not-found' | 'ready' | 'error';

  const pendingStatuses = ['starting', 'recording', 'finalizing'];

  // A running transcript reports progress, so it polls like a pending
  // recording. A waiting one can wait days for a machine to wake: poll it
  // slowly, and stop once nothing is in flight.
  const fastPollMs = 3_000;
  const slowPollMs = 15_000;

  let loadState = $state<LoadState>('loading');
  let room = $state<RoomInfo>();
  let recordings = $state<RecordingInfo[]>([]);
  let recordingsLoading = $state(false);
  let error = $state('');
  let busy = $state(false);
  let copied = $state(false);
  let slugDraft = $state('');
  let slugSaved = $state(false);
  let deleteConfirm = $state(false);
  let deleteRecordingID = $state('');
  let transcriptBusyID = $state('');
  let copyTimer: ReturnType<typeof setTimeout> | undefined;
  let slugSavedTimer: ReturnType<typeof setTimeout> | undefined;
  let recordingPoll: ReturnType<typeof setTimeout> | undefined;
  // Bumped by every local change to the list, so a poll that was already in
  // flight can't put back a deleted recording or a stale transcript.
  let recordingsGeneration = 0;
  let destroyed = false;

  const slug = $derived(page.params.slug ?? '');

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
      error = cause instanceof Error ? cause.message : 'Could not load the room. Reload the page.';
      loadState = 'error';
    }
  }

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

  function schedulePoll(list: RecordingInfo[]): void {
    if (recordingPoll) clearTimeout(recordingPoll);
    recordingPoll = undefined;
    const delay = pollDelay(list);
    if (delay != null && !destroyed) {
      recordingPoll = setTimeout(() => void loadRecordings(), delay);
    }
  }

  async function loadRecordings(): Promise<void> {
    if (destroyed) return;
    const generation = recordingsGeneration;
    recordingsLoading = true;
    try {
      const list = await listRecordings(room?.slug ?? slug);
      if (destroyed || generation !== recordingsGeneration) return;
      recordings = list;
      schedulePoll(list);
    } catch (cause) {
      if (destroyed) return;
      error = cause instanceof Error ? cause.message : 'Could not load recordings. Try again.';
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
      recordings = recordings.map((recording) => ({ ...recording, room_slug: updated.slug }));
      await goto(`/rooms/${updated.slug}`, { replaceState: true });
      slugSaved = true;
      if (slugSavedTimer) clearTimeout(slugSavedTimer);
      slugSavedTimer = setTimeout(() => (slugSaved = false), 1600);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not change the room link. Try again.';
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
      error = cause instanceof Error ? cause.message : 'Could not change the lobby. Try again.';
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
      error = cause instanceof Error ? cause.message : 'Could not delete the room. Try again.';
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
      recordingsGeneration += 1;
      recordings = recordings.filter((recording) => recording.id !== id);
      schedulePoll(recordings);
      deleteRecordingID = '';
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not delete the recording. Try again.';
    } finally {
      busy = false;
    }
  }

  async function transcribe(id: string): Promise<void> {
    if (transcriptBusyID) return;
    transcriptBusyID = id;
    error = '';
    try {
      const transcript = await requestTranscript(id);
      recordingsGeneration += 1;
      recordings = recordings.map((recording) =>
        recording.id === id ? { ...recording, transcript } : recording
      );
      schedulePoll(recordings);
    } catch (cause) {
      error =
        cause instanceof Error ? cause.message : 'Could not request the transcript. Try again.';
    } finally {
      transcriptBusyID = '';
    }
  }

  function percent(progress?: number | null): string {
    if (progress == null) return '';
    return `${Math.round(Math.min(1, Math.max(0, progress)) * 100)}%`;
  }

  function stateText(current: RoomInfo): string {
    if (current.active && current.num_participants > 0) {
      const label = current.num_participants === 1 ? 'person' : 'people';
      return `${current.num_participants} ${label} in the room now`;
    }
    if (current.last_active_at != null) {
      return `Last active ${compactAgo(current.last_active_at)} · created ${compactAgo(current.created_at)}`;
    }
    return `Created ${compactAgo(current.created_at)} · not used yet`;
  }

  function statusTone(status: string): string {
    if (pendingStatuses.includes(status)) return 'text-warn border-warn/40 bg-warn/10';
    if (status === 'completed') return 'text-ok border-ok/30 bg-ok/10';
    if (status === 'failed') return 'text-rec border-rec/30 bg-rec/10';
    return 'text-ink-2 border-border bg-surface';
  }

  onMount(() => void load());
  onDestroy(() => {
    destroyed = true;
    if (copyTimer) clearTimeout(copyTimer);
    if (slugSavedTimer) clearTimeout(slugSavedTimer);
    if (recordingPoll) clearTimeout(recordingPoll);
  });
</script>

<svelte:head>
  <title>{room?.name ?? 'Room'} · klisi</title>
</svelte:head>

{#snippet transcriptCell(id: string, transcript: TranscriptInfo)}
  {#if transcript.status === TranscriptAvailable}
    <button
      type="button"
      disabled={transcriptBusyID !== ''}
      class="inline-flex h-6 shrink-0 items-center gap-1 rounded-control border border-border bg-paper px-2 text-[11px] text-ink transition-colors hover:bg-surface-2 disabled:opacity-60"
      onclick={() => void transcribe(id)}
    >
      <Subtitles size={16} weight="regular" aria-hidden="true" /> Transcribe
    </button>
  {:else if transcript.status === TranscriptWaiting}
    <span
      class="w-max shrink-0 rounded-full border border-border px-1.5 text-[10px] uppercase leading-[18px] text-ink-2"
      title={transcript.message}
    >
      waiting
    </span>
    {#if transcript.message}
      <span class="min-w-0 truncate text-[11px] text-ink-2" title={transcript.message}>
        {transcript.message}
      </span>
    {/if}
  {:else if transcript.status === TranscriptRunning}
    <span
      class="w-max shrink-0 rounded-full border border-warn/40 bg-warn/10 px-1.5 text-[10px] uppercase leading-[18px] text-warn"
      title={transcript.message}
    >
      transcribing
    </span>
    {#if transcript.progress != null}
      <span class="mono shrink-0 text-[11px] text-ink">{percent(transcript.progress)}</span>
    {/if}
    {#if transcript.message}
      <span class="min-w-0 truncate text-[11px] text-ink-2" title={transcript.message}>
        {transcript.message}
      </span>
    {/if}
  {:else if transcript.status === TranscriptCompleted}
    {#if transcript.speakers != null}
      <span class="shrink-0 text-[11px] text-ink-2">
        {transcript.speakers}
        {transcript.speakers === 1 ? 'speaker' : 'speakers'}
      </span>
    {/if}
    <a
      class="inline-flex h-6 shrink-0 items-center gap-1 rounded-control border border-border bg-paper px-2 text-[11px] text-ink no-underline transition-colors hover:bg-surface-2"
      href={transcriptDownloadURL(id, TranscriptFormatText)}
      target="_blank"
      rel="noreferrer"
      title="Download the transcript (.txt)"
    >
      <DownloadSimple size={16} weight="regular" aria-hidden="true" /> Transcript
    </a>
    <a
      class="inline-flex h-6 shrink-0 items-center gap-1 rounded-control border border-border bg-paper px-2 text-[11px] text-ink no-underline transition-colors hover:bg-surface-2"
      href={transcriptDownloadURL(id, TranscriptFormatVTT)}
      target="_blank"
      rel="noreferrer"
      title="Download captions (.vtt)"
    >
      <DownloadSimple size={16} weight="regular" aria-hidden="true" /> Captions
    </a>
  {:else if transcript.status === TranscriptFailed}
    <span
      class="w-max shrink-0 rounded-full border border-rec/30 bg-rec/10 px-1.5 text-[10px] uppercase leading-[18px] text-rec"
      title={transcript.error}
    >
      failed
    </span>
    {#if transcript.error}
      <span class="min-w-0 truncate text-[11px] text-ink-2" title={transcript.error}>
        {transcript.error}
      </span>
    {/if}
    <button
      type="button"
      disabled={transcriptBusyID !== ''}
      class="inline-flex h-6 shrink-0 items-center gap-1 rounded-control border border-border bg-paper px-2 text-[11px] text-ink transition-colors hover:bg-surface-2 disabled:opacity-60"
      onclick={() => void transcribe(id)}
    >
      <ArrowClockwise size={16} weight="regular" aria-hidden="true" /> Retry
    </button>
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
    <!-- Room header -->
    <div class="flex items-start gap-4">
      <div class="w-40 shrink-0">
        <StateTile {room} />
      </div>
      <div class="min-w-0 flex-1">
        <h1 class="truncate text-[18px] font-[550] leading-6 text-ink">{room.name}</h1>
        <div class="mt-1.5 flex flex-wrap items-center gap-2 text-[12px] text-ink-2">
          <span>{stateText(room)}</span>
        </div>
        <div class="mt-3 flex flex-wrap items-center gap-2">
          <Button href={`/m/${room.slug}`} variant="accent">
            Join meeting <ArrowRight size={14} weight="bold" aria-hidden="true" />
          </Button>
          <Button type="button" variant="default" onclick={() => void copyLink()}>
            {#if copied}
              <Check size={16} weight="regular" aria-hidden="true" /> Copied
            {:else}
              <Copy size={16} weight="regular" aria-hidden="true" /> Copy link
            {/if}
          </Button>
        </div>
      </div>
    </div>

    {#if error}<p class="mt-4 text-[12px] text-rec" role="alert">{error}</p>{/if}

    <!-- Lobby setting -->
    <!-- Recordings / past meetings -->
    <section class="mt-8">
      <div class="flex items-center justify-between">
        <h2 class="text-[13px] font-[550] text-ink">Recordings</h2>
        {#if recordings.length > 0}
          <span class="text-[12px] text-ink-2">{recordings.length} total</span>
        {/if}
      </div>

      {#if recordingsLoading && recordings.length === 0}
        <p
          class="mt-2 rounded-card border border-border bg-surface px-3 py-6 text-center text-[12px] text-ink-2"
        >
          Loading recordings…
        </p>
      {:else if recordings.length === 0}
        <p
          class="mt-2 rounded-card border border-border bg-surface px-3 py-6 text-center text-[12px] text-ink-2"
        >
          No recordings yet. They'll appear here after a meeting is recorded.
        </p>
      {:else}
        <div class="mt-2 overflow-hidden rounded-card border border-border bg-surface">
          {#each recordings as recording (recording.id)}
            <div
              class="grid grid-cols-[max-content_minmax(90px,1fr)_auto] items-center gap-x-3 gap-y-2 border-b border-border px-3 py-2.5 last:border-b-0 sm:grid-cols-[max-content_minmax(90px,1fr)_56px_64px_auto] lg:grid-cols-[max-content_minmax(90px,1fr)_56px_64px_236px_auto]"
              data-recording-id={recording.id}
            >
              <span class="flex w-max items-center gap-1">
                <span
                  class="w-max rounded-full border px-1.5 text-[10px] uppercase leading-[18px] {statusTone(
                    recording.status
                  )}"
                >
                  {recording.status}
                </span>
                <span
                  class="mono w-max rounded-full border border-border px-1.5 text-[10px] uppercase leading-[18px] text-ink-2"
                >
                  {recording.audio_only ? 'audio' : 'video'}
                </span>
              </span>
              <time
                class="truncate text-[11px] text-ink-2"
                datetime={new Date(recording.started_at * 1000).toISOString()}
              >
                {relativeDate(recording.started_at)}
              </time>
              <span class="hidden text-[11px] text-ink-2 sm:block">
                {durationLabel(recording.duration_s)}
              </span>
              <span class="hidden text-[11px] text-ink-2 sm:block">
                {sizeLabel(recording.size_bytes)}
              </span>
              <!-- Its own column when there's room; below the row when there isn't. -->
              <div
                class="order-last col-span-full min-w-0 items-center gap-1.5 lg:order-none lg:col-span-1 {recording.transcript
                  ? 'flex'
                  : 'hidden lg:block'}"
                data-testid="transcript"
                data-status={recording.transcript?.status}
              >
                {#if recording.transcript}
                  <span class="sr-only">Transcript:</span>
                  {@render transcriptCell(recording.id, recording.transcript)}
                {/if}
              </div>
              <div class="flex justify-end gap-1">
                {#if recording.status === 'completed'}
                  <a
                    class="inline-flex h-6 items-center gap-1 rounded-control border border-border bg-paper px-2 text-[11px] text-ink no-underline transition-colors hover:bg-surface-2"
                    href={recordingDownloadURL(recording.id)}
                    target="_blank"
                    rel="noreferrer"
                  >
                    <DownloadSimple size={16} weight="regular" aria-hidden="true" /> Download
                  </a>
                {/if}
                <button
                  type="button"
                  disabled={busy || pendingStatuses.includes(recording.status)}
                  class="inline-flex h-6 items-center gap-1 rounded-control border px-2 text-[11px] transition-colors disabled:opacity-60 {deleteRecordingID ===
                  recording.id
                    ? 'border-rec bg-rec text-white'
                    : 'border-border bg-paper text-ink-2 hover:bg-surface-2 hover:text-ink'}"
                  aria-label={deleteRecordingID === recording.id
                    ? 'Confirm delete recording'
                    : 'Delete recording'}
                  onclick={() => void removeRecording(recording.id)}
                >
                  {#if deleteRecordingID === recording.id}
                    Delete?
                  {:else}
                    <Trash size={16} weight="regular" aria-hidden="true" />
                  {/if}
                </button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </section>

    <!-- Danger zone -->

    <section class="mt-8">
      <h2 class="text-[13px] font-[550] text-ink">Settings</h2>
      <div class="bg-surface rounded-control">
        <div class="mt-2 flex items-center justify-between gap-4 px-3 py-2.5">
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
            <span class="mt-0.5 block text-[12px] text-ink-2">
              Lowercase letters, numbers, and hyphens. Changing it invalidates the old link.
            </span>
            <input
              id="room-slug"
              name="room-slug"
              class="mono mt-2 h-7 w-full max-w-sm rounded-control border border-border bg-paper px-2 text-[12px] text-ink outline-none focus:border-accent"
              value={slugDraft}
              minlength="3"
              maxlength="64"
              pattern="[a-z0-9]+(-[a-z0-9]+)*"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              disabled={busy || room.active || room.recording}
              aria-describedby="room-slug-status"
              oninput={(event) => (slugDraft = event.currentTarget.value.toLowerCase())}
              required
            />
            <span id="room-slug-status" class="mt-1 block text-[11px] text-ink-2">
              {#if room.active || room.recording}
                The link can be changed after the meeting and recording stop.
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
              <Check size={14} weight="regular" aria-hidden="true" /> Saved
            {:else}
              Save link
            {/if}
          </button>
        </form>

        <section class="flex items-center justify-between gap-4 p-3">
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
        </section>
      </div>
    </section>
  {/if}
</main>
