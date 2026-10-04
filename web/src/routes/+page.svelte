<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { CalendarPlus, Check, Copy, Desktop, Plus } from 'phosphor-svelte';
  import { AuthRequiredError, listRooms, logout, me } from '$lib/api/client';
  import { AuthLoginPath, type Me, type RoomInfo } from '$lib/api/types.gen';
  import AddToCalendarDialog from '$lib/ui/AddToCalendarDialog.svelte';
  import Button from '$lib/ui/Button.svelte';
  import NewRoomDialog from '$lib/ui/NewRoomDialog.svelte';
  import RoomStatus from '$lib/ui/RoomStatus.svelte';

  type DashboardState = 'loading' | 'signed-out' | 'ready' | 'error';

  let dashboardState = $state<DashboardState>('loading');
  let currentUser = $state<Me>();
  let rooms = $state<RoomInfo[]>([]);
  let error = $state('');
  let newRoomOpen = $state(false);
  let calendarOpen = $state(false);
  let calendarRoom = $state<RoomInfo>();
  let copiedSlug = $state('');
  let copyTimer: ReturnType<typeof setTimeout> | undefined;
  let refreshTimer: ReturnType<typeof setInterval> | undefined;
  let refreshing = false;

  // How often the dashboard re-fetches live room state (participant counts,
  // recording, active). LiveKit's ListRooms lags a join by a few seconds, so a
  // short poll keeps the tiles current without a manual reload.
  const refreshIntervalMs = 5000;

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

  function created(room: RoomInfo): void {
    rooms = [room, ...rooms.filter((item) => item.id !== room.id)];
  }

  function addToCalendar(room: RoomInfo): void {
    calendarRoom = room;
    calendarOpen = true;
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

  // Silent background refresh — updates the live state in place without ever
  // flipping the view back into a loading/error state, and keeps the last good
  // list on a transient failure.
  async function refreshRooms(): Promise<void> {
    if (refreshing || dashboardState !== 'ready') return;
    if (typeof document !== 'undefined' && document.visibilityState !== 'visible') return;
    refreshing = true;
    try {
      rooms = await listRooms();
    } catch {
      // Keep the current list; the next tick retries.
    } finally {
      refreshing = false;
    }
  }

  onMount(() => {
    void loadDashboard();
    refreshTimer = setInterval(() => void refreshRooms(), refreshIntervalMs);
  });
  onDestroy(() => {
    if (copyTimer) clearTimeout(copyTimer);
    if (refreshTimer) clearInterval(refreshTimer);
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
        {#if currentUser?.transcripts}
          <a class="icon-link" href="/machines" aria-label="Machines" title="Machines">
            <Desktop size={16} weight="regular" aria-hidden="true" />
          </a>
        {/if}
        <span>{currentUser?.name}</span>
        <button type="button" onclick={() => void signOut()}>Sign out</button>
      </div>
    </header>

    <main class="dashboard">
      <section aria-labelledby="rooms-heading">
        <div class="section-heading">
          <h1 id="rooms-heading">Rooms</h1>
          <Button variant="accent" onclick={() => (newRoomOpen = true)}>
            <Plus size={16} weight="regular" aria-hidden="true" /> New
          </Button>
        </div>

        {#if rooms.length === 0}
          <div class="empty-state">Create your first room to get a reusable meeting link.</div>
        {:else}
          <ul class="m-0 grid list-none grid-cols-1 gap-2 p-0 sm:grid-cols-2 lg:grid-cols-3">
            {#each rooms as room (room.id)}
              <li class="min-w-0">
                <article
                  data-testid="room-card"
                  data-slug={room.slug}
                  class="group relative flex h-full flex-col gap-3 rounded-card border border-border bg-surface p-3"
                >
                  <!-- Stretched link: the whole card opens the detail page, while
                       the Copy link/Join meeting controls sit above it (z-10) so
                       they act on their own. -->
                  <a
                    href={`/rooms/${room.slug}`}
                    class="absolute inset-0 z-0 rounded-card"
                    aria-label={`Open ${room.name}`}
                  ></a>

                  <div class="min-w-0">
                    <strong
                      class="block truncate text-[13px] font-[550] text-ink transition-colors group-hover:text-accent"
                    >
                      {room.name}
                    </strong>
                    <div class="mt-1"><RoomStatus {room} /></div>
                  </div>

                  <div class="relative z-10 mt-auto flex flex-wrap items-center gap-1.5">
                    <Button
                      type="button"
                      onclick={() => void copyLink(room.slug)}
                      aria-label={`Copy link for ${room.name}`}
                    >
                      {#if copiedSlug === room.slug}
                        <Check size={16} weight="regular" aria-hidden="true" /> Copied
                      {:else}
                        <Copy size={16} weight="regular" aria-hidden="true" /> Copy link
                      {/if}
                    </Button>
                    <Button href={`/m/${room.slug}`}>Join meeting</Button>
                    <button
                      type="button"
                      class="ml-auto grid size-7 place-items-center rounded-control border-0 bg-transparent p-0 text-ink-2 transition-colors hover:bg-surface-2 hover:text-ink"
                      onclick={() => addToCalendar(room)}
                      aria-label={`Add ${room.name} to calendar`}
                      title="Add to calendar…"
                    >
                      <CalendarPlus size={16} weight="regular" aria-hidden="true" />
                    </button>
                  </div>
                </article>
              </li>
            {/each}
          </ul>
        {/if}
        {#if error}<p class="error" role="alert">{error}</p>{/if}
      </section>
    </main>
  </div>

  <NewRoomDialog bind:open={newRoomOpen} oncreated={created} />
  <AddToCalendarDialog bind:open={calendarOpen} room={calendarRoom} />
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

  .sign-in-card button {
    height: var(--control-height);
    padding: 3px 8px;
    color: var(--ink);
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  .sign-in-card button:hover:not(:disabled) {
    background: var(--surface-2);
  }

  .sign-in-card button.primary {
    color: white;
    font-weight: 550;
    background: var(--accent);
    border-color: var(--accent);
  }

  .sign-in-card button.primary:hover:not(:disabled) {
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

  .account button {
    height: var(--control-height);
    padding: 3px 8px;
    color: var(--ink);
    background: var(--paper);
    border: 1px solid var(--border);
    border-radius: var(--radius-control);
  }

  .account button:hover {
    background: var(--surface-2);
  }

  .icon-link {
    display: grid;
    width: var(--control-height);
    height: var(--control-height);
    place-items: center;
    color: var(--ink-2);
    border-radius: var(--radius-control);
    transition:
      color var(--motion-fast),
      background var(--motion-fast);
  }

  .icon-link:hover {
    color: var(--ink);
    background: var(--surface-2);
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
</style>
