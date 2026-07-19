<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { CaretDown, Copy, DownloadSimple, Trash } from 'phosphor-svelte';
  import {
    AuthRequiredError,
    createRoom,
    deleteRecording,
    deleteRoom,
    listRecordings,
    listRooms,
    logout,
    me,
    recordingDownloadURL,
    updateRoom
  } from '$lib/api/client';
  import { AuthLoginPath, type Me, type RecordingInfo, type RoomInfo } from '$lib/api/types.gen';

  type DashboardState = 'loading' | 'signed-out' | 'ready' | 'error';

  let dashboardState = $state<DashboardState>('loading');
  let currentUser = $state<Me>();
  let rooms = $state<RoomInfo[]>([]);
  let roomName = $state('');
  let error = $state('');
  let creating = $state(false);
  let changingSlug = $state('');
  let deleteConfirmSlug = $state('');
  let copiedSlug = $state('');
  let expandedRecordingsSlug = $state('');
  let loadingRecordingsSlug = $state('');
  let recordingsByRoom = $state<Record<string, RecordingInfo[]>>({});
  let deleteConfirmRecordingID = $state('');
  let copyTimer: ReturnType<typeof setTimeout> | undefined;
  let recordingPoll: ReturnType<typeof setTimeout> | undefined;
  // Guards in-flight listRecordings resolutions from rescheduling the poll
  // after the dashboard is gone (review finding #15).
  let destroyed = false;

  async function loadDashboard(): Promise<void> {
    dashboardState = 'loading';
    error = '';
    try {
      currentUser = await me();
      rooms = await listRooms();
      dashboardState = 'ready';
    } catch (cause) {
      if (cause instanceof AuthRequiredError) {
        dashboardState = 'signed-out';
        return;
      }
      error =
        cause instanceof Error ? cause.message : 'Could not load the dashboard. Reload the page.';
      dashboardState = 'error';
    }
  }

  function signIn(): void {
    window.location.href = `${AuthLoginPath}?next=/`;
  }

  async function signOut(): Promise<void> {
    error = '';
    try {
      await logout();
      window.location.reload();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not sign out. Try again.';
    }
  }

  async function create(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const name = roomName.trim();
    if (!name || creating) return;
    creating = true;
    error = '';
    try {
      const room = await createRoom(name);
      rooms = [room, ...rooms];
      roomName = '';
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not create the room. Try again.';
    } finally {
      creating = false;
    }
  }

  async function toggleLobby(room: RoomInfo, enabled: boolean): Promise<void> {
    if (changingSlug) return;
    changingSlug = room.slug;
    error = '';
    try {
      const updated = await updateRoom(room.slug, { lobby_enabled: enabled });
      rooms = rooms.map((item) => (item.slug === updated.slug ? updated : item));
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not change the lobby. Try again.';
    } finally {
      changingSlug = '';
    }
  }

  async function removeRoom(room: RoomInfo): Promise<void> {
    if (deleteConfirmSlug !== room.slug) {
      deleteConfirmSlug = room.slug;
      return;
    }
    changingSlug = room.slug;
    error = '';
    try {
      await deleteRoom(room.slug);
      rooms = rooms.filter((item) => item.slug !== room.slug);
      deleteConfirmSlug = '';
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not delete the room. Try again.';
    } finally {
      changingSlug = '';
    }
  }

  async function copyLink(slug: string): Promise<void> {
    error = '';
    try {
      await navigator.clipboard.writeText(`${window.location.origin}/m/${slug}`);
      copiedSlug = slug;
      if (copyTimer) clearTimeout(copyTimer);
      copyTimer = setTimeout(() => (copiedSlug = ''), 1600);
    } catch {
      error = 'Could not copy the meeting link. Copy it from the address bar instead.';
    }
  }

  async function toggleRecordings(slug: string): Promise<void> {
    if (expandedRecordingsSlug === slug) {
      expandedRecordingsSlug = '';
      if (recordingPoll) clearTimeout(recordingPoll);
      recordingPoll = undefined;
      return;
    }
    expandedRecordingsSlug = slug;
    deleteConfirmRecordingID = '';
    await loadRoomRecordings(slug);
  }

  async function loadRoomRecordings(slug: string): Promise<void> {
    if (destroyed || expandedRecordingsSlug !== slug) return;
    loadingRecordingsSlug = slug;
    try {
      const recordings = await listRecordings(slug);
      // The await may resolve after teardown or after the user collapsed or
      // switched rooms — never store results or reschedule in that case.
      if (destroyed || expandedRecordingsSlug !== slug) return;
      recordingsByRoom = { ...recordingsByRoom, [slug]: recordings };
      if (
        recordings.some((recording) =>
          ['starting', 'recording', 'finalizing'].includes(recording.status)
        )
      ) {
        if (recordingPoll) clearTimeout(recordingPoll);
        recordingPoll = setTimeout(() => void loadRoomRecordings(slug), 3_000);
      }
    } catch (cause) {
      if (destroyed) return;
      error = cause instanceof Error ? cause.message : 'Could not load recordings. Try again.';
    } finally {
      loadingRecordingsSlug = '';
    }
  }

  async function removeRecording(slug: string, id: string): Promise<void> {
    if (deleteConfirmRecordingID !== id) {
      deleteConfirmRecordingID = id;
      return;
    }
    changingSlug = slug;
    error = '';
    try {
      await deleteRecording(id);
      recordingsByRoom = {
        ...recordingsByRoom,
        [slug]: (recordingsByRoom[slug] ?? []).filter((recording) => recording.id !== id)
      };
      deleteConfirmRecordingID = '';
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not delete the recording. Try again.';
    } finally {
      changingSlug = '';
    }
  }

  function relativeDate(timestamp: number): string {
    const seconds = Math.round(timestamp - Date.now() / 1000);
    const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
    if (Math.abs(seconds) < 60) return formatter.format(seconds, 'second');
    const minutes = Math.round(seconds / 60);
    if (Math.abs(minutes) < 60) return formatter.format(minutes, 'minute');
    const hours = Math.round(minutes / 60);
    if (Math.abs(hours) < 24) return formatter.format(hours, 'hour');
    return formatter.format(Math.round(hours / 24), 'day');
  }

  function durationLabel(seconds?: number | null): string {
    if (seconds == null) return '—';
    const minutes = Math.floor(seconds / 60);
    const remainder = seconds % 60;
    return `${minutes}:${remainder.toString().padStart(2, '0')}`;
  }

  function sizeLabel(bytes?: number | null): string {
    if (bytes == null) return '—';
    if (bytes < 1_000_000) return `${Math.max(1, Math.round(bytes / 1_000))} KB`;
    return `${(bytes / 1_000_000).toFixed(1)} MB`;
  }

  onMount(() => void loadDashboard());
  onDestroy(() => {
    destroyed = true;
    if (copyTimer) clearTimeout(copyTimer);
    if (recordingPoll) clearTimeout(recordingPoll);
  });
</script>

<svelte:head>
  <title>klisi</title>
  <meta name="description" content="Lean self-hosted video meetings" />
</svelte:head>

{#if dashboardState === 'loading'}
  <main class="center-state" aria-live="polite"><p>Loading…</p></main>
{:else if dashboardState === 'signed-out'}
  <main class="center-state">
    <section class="sign-in-card">
      <div class="wordmark">klisi</div>
      <p>Create a room and meet without the clutter.</p>
      <button class="primary" type="button" onclick={signIn}>Continue with SSO</button>
    </section>
  </main>
{:else if dashboardState === 'error'}
  <main class="center-state">
    <section class="sign-in-card">
      <div class="wordmark">klisi</div>
      <p role="alert">{error}</p>
      <button type="button" onclick={() => void loadDashboard()}>Try again</button>
    </section>
  </main>
{:else}
  <div class="dashboard-shell">
    <header>
      <a class="wordmark" href="/">klisi</a>
      <div class="account">
        <span>{currentUser?.name}</span>
        <button type="button" onclick={() => void signOut()}>Sign out</button>
      </div>
    </header>

    <main class="dashboard">
      <section aria-labelledby="rooms-heading">
        <div class="section-heading">
          <h1 id="rooms-heading">Rooms</h1>
          <form class="new-room" onsubmit={create}>
            <label class="sr-only" for="room-name">Room name</label>
            <input
              id="room-name"
              name="room-name"
              bind:value={roomName}
              placeholder="Room name"
              maxlength="100"
              required
            />
            <button class="primary" type="submit" disabled={creating}>
              {creating ? 'Creating…' : 'New room'}
            </button>
          </form>
        </div>

        {#if rooms.length === 0}
          <div class="empty-state">Create your first room to get a reusable meeting link.</div>
        {:else}
          <div class="room-list">
            {#each rooms as room (room.id)}
              <div class="room-entry">
                <article class="room-row">
                  <a class="room-link" href={`/m/${room.slug}`}>
                    <strong>{room.name}</strong>
                    <span class="slug mono">{room.slug}</span>
                  </a>
                  <div class="row-actions">
                    <button
                      class="icon-button recordings-toggle"
                      class:expanded={expandedRecordingsSlug === room.slug}
                      type="button"
                      aria-expanded={expandedRecordingsSlug === room.slug}
                      aria-label={`${expandedRecordingsSlug === room.slug ? 'Hide' : 'Show'} recordings for ${room.name}`}
                      title="Recordings"
                      onclick={() => void toggleRecordings(room.slug)}
                    >
                      <CaretDown size={16} weight="regular" aria-hidden="true" />
                    </button>
                    <button
                      class="icon-button copy"
                      type="button"
                      aria-label={`Copy link for ${room.name}`}
                      title="Copy meeting link"
                      onclick={() => void copyLink(room.slug)}
                    >
                      <Copy size={16} weight="regular" aria-hidden="true" />
                      {#if copiedSlug === room.slug}<span class="copied">Copied</span>{/if}
                    </button>
                    <button
                      class="lobby-toggle"
                      class:on={room.lobby_enabled}
                      type="button"
                      role="switch"
                      aria-checked={room.lobby_enabled}
                      aria-label="Lobby"
                      disabled={changingSlug === room.slug}
                      onclick={() => void toggleLobby(room, !room.lobby_enabled)}
                    >
                      <span class="switch-track" aria-hidden="true"><span></span></span>
                      <span>Lobby</span>
                    </button>
                    <button
                      class:confirm-delete={deleteConfirmSlug === room.slug}
                      class="delete-button"
                      type="button"
                      disabled={changingSlug === room.slug}
                      aria-label={deleteConfirmSlug === room.slug
                        ? `Confirm delete ${room.name}`
                        : `Delete ${room.name}`}
                      onclick={() => void removeRoom(room)}
                    >
                      {#if deleteConfirmSlug === room.slug}
                        Delete?
                      {:else}
                        <Trash size={16} weight="regular" aria-hidden="true" />
                      {/if}
                    </button>
                  </div>
                </article>
                {#if expandedRecordingsSlug === room.slug}
                  <section class="recordings" aria-label={`Recordings for ${room.name}`}>
                    {#if loadingRecordingsSlug === room.slug && !recordingsByRoom[room.slug]}
                      <p class="recordings-state">Loading recordings…</p>
                    {:else if (recordingsByRoom[room.slug] ?? []).length === 0}
                      <p class="recordings-state">No recordings yet.</p>
                    {:else}
                      {#each recordingsByRoom[room.slug] ?? [] as recording (recording.id)}
                        <div class="recording-row" data-recording-id={recording.id}>
                          <span
                            class:pending={['starting', 'recording', 'finalizing'].includes(
                              recording.status
                            )}
                            class:completed={recording.status === 'completed'}
                            class:failed={recording.status === 'failed'}
                            class="recording-status mono"
                          >
                            {recording.status}
                          </span>
                          <time
                            class="mono"
                            datetime={new Date(recording.started_at * 1000).toISOString()}
                          >
                            {relativeDate(recording.started_at)}
                          </time>
                          <span class="mono">{durationLabel(recording.duration_s)}</span>
                          <span class="mono">{sizeLabel(recording.size_bytes)}</span>
                          <div class="recording-actions">
                            {#if recording.status === 'completed'}
                              <a
                                class="download-button"
                                href={recordingDownloadURL(recording.id)}
                                target="_blank"
                                rel="noreferrer"
                              >
                                <DownloadSimple size={16} weight="regular" aria-hidden="true" />
                                Download
                              </a>
                            {/if}
                            <button
                              class:confirm-delete={deleteConfirmRecordingID === recording.id}
                              class="recording-delete"
                              type="button"
                              disabled={changingSlug === room.slug ||
                                ['starting', 'recording', 'finalizing'].includes(recording.status)}
                              aria-label={deleteConfirmRecordingID === recording.id
                                ? 'Delete recording?'
                                : 'Delete recording'}
                              onclick={() => void removeRecording(room.slug, recording.id)}
                            >
                              {deleteConfirmRecordingID === recording.id ? 'Delete?' : 'Delete'}
                            </button>
                          </div>
                        </div>
                      {/each}
                    {/if}
                  </section>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
        {#if error}<p class="error" role="alert">{error}</p>{/if}
      </section>
    </main>
  </div>
{/if}

<style>
  .center-state {
    display: grid;
    min-height: 100dvh;
    padding: 24px 12px;
    place-items: center;
    color: var(--ink-2);
  }

  .center-state > p {
    margin: 0;
    font-size: 12px;
  }

  .sign-in-card {
    width: min(100%, 340px);
    padding: 24px;
    color: var(--ink);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-card);
  }

  .sign-in-card p {
    margin: 8px 0 20px;
    color: var(--ink-2);
  }

  .wordmark {
    color: var(--accent);
    font-size: 15px;
    font-weight: 550;
    letter-spacing: 0.02em;
    text-decoration: none;
  }

  button,
  input {
    height: var(--control-height);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  button {
    padding: 3px 8px;
    color: var(--ink);
    background: var(--paper);
  }

  button:hover:not(:disabled) {
    background: var(--surface-2);
  }

  button:disabled {
    cursor: wait;
    opacity: 0.6;
  }

  button.primary {
    color: white;
    font-weight: 550;
    background: var(--accent);
    border-color: var(--accent);
  }

  button.primary:hover:not(:disabled) {
    background: var(--accent-hover);
    border-color: var(--accent-hover);
  }

  .dashboard-shell {
    min-height: 100dvh;
  }

  header {
    display: flex;
    height: 48px;
    align-items: center;
    justify-content: space-between;
    padding: 0 16px;
    border-bottom: 1px solid var(--border);
  }

  .account {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--ink-2);
    font-size: 12px;
  }

  .dashboard {
    width: min(100%, 880px);
    margin: 0 auto;
    padding: 32px 16px;
  }

  .section-heading {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 12px;
  }

  h1 {
    margin: 0;
    font-size: 18px;
    line-height: 24px;
    font-weight: 550;
  }

  .new-room {
    display: flex;
    gap: 4px;
  }

  .new-room input {
    width: 200px;
    min-width: 0;
    padding: 3px 7px;
    color: var(--ink);
    background: var(--surface);
  }

  .room-list {
    overflow: visible;
    border: 1px solid var(--border);
    border-radius: var(--radius-card);
  }

  .room-row {
    display: flex;
    min-height: 52px;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 8px 10px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    transition: background var(--motion-fast);
  }

  .room-row:hover,
  .room-row:focus-within {
    background: var(--surface-2);
  }

  .room-entry {
    background: var(--surface);
    border-bottom: 1px solid var(--border);
  }

  .room-entry:last-child {
    border-bottom: 0;
    border-radius: 0 0 var(--radius-card) var(--radius-card);
  }

  .room-entry:only-child {
    border-radius: var(--radius-card);
  }

  .room-entry .room-row {
    border-bottom: 0;
    border-radius: 0;
  }

  .room-row:first-child {
    border-radius: var(--radius-card) var(--radius-card) 0 0;
  }

  .room-row:last-child {
    border-bottom: 0;
    border-radius: 0 0 var(--radius-card) var(--radius-card);
  }

  .room-row:only-child {
    border-radius: var(--radius-card);
  }

  .room-link {
    display: flex;
    min-width: 0;
    flex: 1;
    align-items: center;
    gap: 10px;
    color: var(--ink);
    text-decoration: none;
  }

  .room-link:hover strong {
    color: var(--accent);
  }

  .room-link strong {
    overflow: hidden;
    font-weight: 550;
    text-overflow: ellipsis;
    white-space: nowrap;
    transition: color var(--motion-fast);
  }

  .slug {
    flex: none;
    padding: 1px 7px;
    color: var(--ink-2);
    font-size: 11px;
    line-height: 18px;
    border: 1px solid var(--border);
    border-radius: 999px;
  }

  .row-actions {
    display: flex;
    flex: none;
    align-items: center;
    gap: 6px;
  }

  .icon-button,
  .delete-button {
    display: grid;
    min-width: 30px;
    padding: 0 6px;
    place-items: center;
  }

  .recordings-toggle svg {
    transition: transform var(--motion-fast);
  }

  .recordings-toggle.expanded svg {
    transform: rotate(180deg);
  }

  .recordings {
    padding: 0 10px 8px;
  }

  .recordings-state {
    margin: 0;
    padding: 10px;
    color: var(--ink-2);
    font-size: 12px;
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  .recording-row {
    display: grid;
    min-height: 38px;
    grid-template-columns: 82px minmax(100px, 1fr) 58px 72px auto;
    align-items: center;
    gap: 8px;
    padding: 5px 7px;
    color: var(--ink-2);
    font-size: 11px;
    background: var(--paper);
    border: 1px solid var(--border);
    border-bottom: 0;
  }

  .recording-row:first-child {
    border-radius: var(--radius-control) var(--radius-control) 0 0;
  }

  .recording-row:last-child {
    border-bottom: 1px solid var(--border);
    border-radius: 0 0 var(--radius-control) var(--radius-control);
  }

  .recording-row:only-child {
    border-radius: var(--radius-control);
  }

  .recording-status {
    width: max-content;
    padding: 0 5px;
    color: var(--ink-2);
    font-size: 10px;
    line-height: 18px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 999px;
    text-transform: uppercase;
  }

  .recording-status.pending {
    color: color-mix(in srgb, var(--warn) 76%, var(--ink));
    background: color-mix(in srgb, var(--warn) 12%, var(--paper));
    border-color: color-mix(in srgb, var(--warn) 35%, var(--border));
  }

  .recording-status.completed {
    color: var(--ok);
    background: color-mix(in srgb, var(--ok) 10%, var(--paper));
    border-color: color-mix(in srgb, var(--ok) 30%, var(--border));
  }

  .recording-status.failed {
    color: var(--rec);
    background: color-mix(in srgb, var(--rec) 9%, var(--paper));
    border-color: color-mix(in srgb, var(--rec) 28%, var(--border));
  }

  .recording-actions {
    display: flex;
    justify-content: flex-end;
    gap: 4px;
  }

  .download-button,
  .recording-delete {
    display: inline-flex;
    height: 24px;
    align-items: center;
    gap: 4px;
    padding: 1px 6px;
    color: var(--ink);
    font-size: 11px;
    line-height: 20px;
    text-decoration: none;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  .recording-delete {
    color: var(--ink-2);
  }

  .recording-delete.confirm-delete {
    color: white;
    background: var(--rec);
    border-color: var(--rec);
  }

  .copy {
    position: relative;
  }

  .copied {
    position: absolute;
    right: 0;
    bottom: calc(100% + 4px);
    padding: 1px 5px;
    color: var(--text);
    font-size: 11px;
    background: var(--panel);
    border-radius: var(--radius-control);
  }

  .lobby-toggle {
    display: flex;
    height: var(--control-height);
    align-items: center;
    gap: 5px;
    padding: 3px 7px;
    color: var(--ink-2);
    background: var(--paper);
    font-size: 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  .switch-track {
    display: flex;
    width: 22px;
    height: 12px;
    align-items: center;
    padding: 1px;
    background: var(--border);
    border-radius: 999px;
    transition: background var(--motion-fast);
  }

  .switch-track span {
    width: 8px;
    height: 8px;
    background: var(--paper);
    border-radius: 999px;
    transition: transform var(--motion-fast);
  }

  .lobby-toggle.on .switch-track {
    background: var(--accent);
  }

  .lobby-toggle.on .switch-track span {
    transform: translateX(10px);
  }

  .delete-button {
    color: var(--ink-2);
  }

  .delete-button.confirm-delete {
    display: block;
    color: white;
    background: var(--rec);
    border-color: var(--rec);
  }

  .empty-state {
    padding: 24px 12px;
    color: var(--ink-2);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-card);
    text-align: center;
  }

  .error {
    margin: 8px 0 0;
    color: var(--rec);
    font-size: 12px;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  @media (max-width: 640px) {
    .section-heading,
    .room-row,
    .room-link {
      align-items: stretch;
    }

    .section-heading,
    .room-row {
      flex-direction: column;
    }

    .new-room input {
      width: 100%;
    }

    .new-room {
      width: 100%;
    }

    .new-room input {
      flex: 1;
    }

    .room-link {
      flex-direction: column;
      gap: 4px;
    }

    .row-actions {
      justify-content: flex-end;
    }

    .recording-row {
      grid-template-columns: 1fr 1fr;
    }

    .recording-actions {
      grid-column: 1 / -1;
      justify-content: flex-start;
    }
  }
</style>
