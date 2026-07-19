<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { Copy, Trash } from 'phosphor-svelte';
  import {
    AuthRequiredError,
    createRoom,
    deleteRoom,
    listRooms,
    logout,
    me,
    updateRoom
  } from '$lib/api/client';
  import { AuthLoginPath, type Me, type RoomInfo } from '$lib/api/types.gen';

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
  let copyTimer: ReturnType<typeof setTimeout> | undefined;

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
      error = cause instanceof Error ? cause.message : 'Could not load the dashboard. Reload the page.';
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

  onMount(() => void loadDashboard());
  onDestroy(() => {
    if (copyTimer) clearTimeout(copyTimer);
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
              <article class="room-row">
                <a class="room-link" href={`/m/${room.slug}`}>
                  <strong>{room.name}</strong>
                  <span class="slug mono">{room.slug}</span>
                </a>
                <div class="row-actions">
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
                  <label class="lobby-toggle">
                    <input
                      type="checkbox"
                      checked={room.lobby_enabled}
                      disabled={changingSlug === room.slug}
                      onchange={(event) =>
                        void toggleLobby(room, (event.currentTarget as HTMLInputElement).checked)}
                    />
                    <span>Lobby</span>
                  </label>
                  <button
                    class:confirm-delete={deleteConfirmSlug === room.slug}
                    class="delete-button"
                    type="button"
                    disabled={changingSlug === room.slug}
                    aria-label={deleteConfirmSlug === room.slug ? `Confirm delete ${room.name}` : `Delete ${room.name}`}
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
    font-size: 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  .lobby-toggle input {
    width: 13px;
    height: 13px;
    margin: 0;
    accent-color: var(--accent);
  }

  .delete-button {
    color: var(--rec);
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
  }
</style>
