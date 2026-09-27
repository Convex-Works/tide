<script lang="ts">
  import { onDestroy, onMount, untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import {
    ArrowLeft,
    Check,
    CheckCircle,
    Copy,
    Info,
    LinkBreak,
    Plus,
    SignIn
  } from 'phosphor-svelte';
  import {
    ApiError,
    AuthRequiredError,
    confirmPairing,
    denyPairing,
    listMachines,
    pairing,
    removeMachine
  } from '$lib/api/client';
  import {
    AuthLoginPath,
    type MachineInfo,
    type MachinesResponse,
    type PairingInfo
  } from '$lib/api/types.gen';
  import { compactAgo, shortHash, systemLabel } from '$lib/format';

  // One route, two jobs (ARCHITECTURE.md §8.1): with ?code= the moil app sent
  // the host here to confirm a pairing; without one it lists their machines.
  type View = 'loading' | 'signed-out' | 'error' | 'confirm' | 'expired' | 'denied' | 'list';

  // Machines come online, go offline and get approved in the moil app while
  // this page is open, so the list refreshes itself quietly.
  const refreshIntervalMs = 10_000;

  let view = $state<View>('loading');
  let pending = $state<PairingInfo>();
  let data = $state<MachinesResponse>();
  let paired = $state<MachineInfo>();
  let deniedName = $state('');
  let error = $state('');
  let busy = $state(false);
  let unpairID = $state('');
  let copied = $state(false);
  let copyTimer: ReturnType<typeof setTimeout> | undefined;
  let refreshTimer: ReturnType<typeof setInterval> | undefined;
  let refreshing = false;
  let destroyed = false;
  // The code the current view was loaded for; plain, so writing it never
  // re-runs the effect below.
  let shownCode: string | undefined;

  const code = $derived(page.url.searchParams.get('code')?.trim() ?? '');
  const signInHref = $derived(
    `${AuthLoginPath}?next=${encodeURIComponent(page.url.pathname + page.url.search)}`
  );
  const pairHref = $derived(data ? `moil://pair?url=${encodeURIComponent(data.moil_url)}` : '');

  function failed(cause: unknown, fallback: string): string {
    return cause instanceof Error ? cause.message : fallback;
  }

  async function load(current: string): Promise<void> {
    view = 'loading';
    error = '';
    unpairID = '';
    if (current) paired = undefined;
    try {
      if (current) {
        const waiting = await pairing(current);
        if (destroyed || current !== shownCode) return;
        pending = waiting;
        view = 'confirm';
      } else {
        const list = await listMachines();
        if (destroyed || current !== shownCode) return;
        data = list;
        view = 'list';
      }
    } catch (cause) {
      if (destroyed || current !== shownCode) return;
      if (cause instanceof AuthRequiredError) {
        view = 'signed-out';
      } else if (current && cause instanceof ApiError && cause.status === 404) {
        view = 'expired';
      } else {
        error = failed(cause, 'Could not load your machines. Reload the page.');
        view = 'error';
      }
    }
  }

  $effect(() => {
    const current = code;
    if (current === shownCode) return;
    shownCode = current;
    untrack(() => void load(current));
  });

  function retry(): void {
    shownCode = code;
    void load(code);
  }

  async function confirm(): Promise<void> {
    if (!pending || busy) return;
    const current = pending;
    busy = true;
    error = '';
    try {
      paired = await confirmPairing(current.code);
      // The code is spent: drop it from the address so a reload shows the
      // list rather than an expired code.
      shownCode = '';
      void goto('/machines', { replaceState: true, keepFocus: true, noScroll: true });
      data = await listMachines();
      if (destroyed) return;
      pending = undefined;
      view = 'list';
    } catch (cause) {
      if (cause instanceof AuthRequiredError) {
        view = 'signed-out';
      } else if (!paired && cause instanceof ApiError && cause.status === 404) {
        view = 'expired';
      } else if (paired) {
        // Paired, but the list didn't load: say so rather than hide the
        // success. Trying again loads the list under the paired banner.
        error = `Paired ${paired.name}, but your machines didn't load. Try again.`;
        view = 'error';
      } else {
        error = failed(cause, 'Could not pair the machine. Try again.');
      }
    } finally {
      busy = false;
    }
  }

  async function deny(): Promise<void> {
    if (!pending || busy) return;
    const current = pending;
    busy = true;
    error = '';
    try {
      await denyPairing(current.code);
      deniedName = current.name;
      pending = undefined;
      view = 'denied';
    } catch (cause) {
      if (cause instanceof AuthRequiredError) {
        view = 'signed-out';
      } else if (cause instanceof ApiError && cause.status === 404) {
        view = 'expired';
      } else {
        error = failed(cause, 'Could not deny the machine. Try again.');
      }
    } finally {
      busy = false;
    }
  }

  async function unpair(id: string): Promise<void> {
    if (!data || busy) return;
    if (unpairID !== id) {
      unpairID = id;
      return;
    }
    busy = true;
    error = '';
    try {
      await removeMachine(id);
      if (data) data.machines = data.machines.filter((machine) => machine.id !== id);
      if (paired?.id === id) paired = undefined;
      unpairID = '';
    } catch (cause) {
      error = failed(cause, 'Could not unpair the machine. Try again.');
    } finally {
      busy = false;
    }
  }

  async function copyAddress(): Promise<void> {
    if (!data) return;
    error = '';
    try {
      await navigator.clipboard.writeText(data.moil_url);
      copied = true;
      if (copyTimer) clearTimeout(copyTimer);
      copyTimer = setTimeout(() => (copied = false), 1600);
    } catch {
      error = 'Could not copy the address. Select it and copy it instead.';
    }
  }

  // Silent background refresh: keeps the last good list on a failure and
  // never flips the view back to loading.
  async function refresh(): Promise<void> {
    if (refreshing || busy || view !== 'list') return;
    if (document.visibilityState !== 'visible') return;
    refreshing = true;
    try {
      const list = await listMachines();
      if (!destroyed && view === 'list' && !busy) data = list;
    } catch {
      // The next tick retries.
    } finally {
      refreshing = false;
    }
  }

  function online(machine: MachineInfo): boolean {
    return machine.state === 'idle' || machine.state === 'busy';
  }

  function stateLabel(machine: MachineInfo): string {
    if (machine.state === 'idle') return 'Online';
    if (machine.state === 'busy') return 'Busy';
    if (machine.state === 'paused') return 'Paused';
    const seen = machine.last_seen_at;
    return seen != null ? `Offline · last seen ${compactAgo(seen)}` : 'Never connected';
  }

  function stateDot(machine: MachineInfo): string {
    if (online(machine)) return 'bg-ok';
    if (machine.state === 'paused') return 'bg-warn';
    return 'border border-ink-2 bg-transparent';
  }

  onMount(() => {
    refreshTimer = setInterval(() => void refresh(), refreshIntervalMs);
  });
  onDestroy(() => {
    destroyed = true;
    if (copyTimer) clearTimeout(copyTimer);
    if (refreshTimer) clearInterval(refreshTimer);
  });
</script>

<svelte:head>
  <title>Machines · klisi</title>
</svelte:head>

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
  {#if view === 'loading'}
    <p class="m-0 text-[12px] text-ink-2" aria-live="polite">Loading…</p>
  {:else if view === 'signed-out'}
    <section class="mx-auto w-[min(100%,400px)] rounded-card border border-border bg-surface p-4">
      <h1 class="m-0 text-[15px] font-[550] leading-6 text-ink">
        {code ? 'Sign in to pair this machine' : 'Sign in to see your machines'}
      </h1>
      <p class="m-0 mt-1 text-[12px] text-ink-2">
        {code
          ? 'The machine will belong to the account you sign in with.'
          : 'Machines you pair with klisi transcribe your recordings.'}
      </p>
      <a
        class="mt-4 inline-flex h-7 items-center gap-1.5 rounded-control border border-accent bg-accent px-2.5 text-[12px] font-[550] text-white no-underline transition-colors hover:border-accent-hover hover:bg-accent-hover"
        href={signInHref}
        data-sveltekit-reload
      >
        <SignIn size={16} weight="regular" aria-hidden="true" /> Sign in
      </a>
    </section>
  {:else if view === 'error'}
    <div class="rounded-card border border-border bg-surface px-4 py-8 text-center">
      <p class="m-0 text-[13px] text-rec" role="alert">{error}</p>
      <button
        class="mt-2 border-0 bg-transparent p-0 text-[12px] text-accent"
        type="button"
        onclick={retry}
      >
        Try again
      </button>
    </div>
  {:else if view === 'confirm' && pending}
    <section
      class="mx-auto w-[min(100%,400px)] rounded-card border border-border bg-surface"
      aria-labelledby="pair-heading"
    >
      <div class="p-4">
        <h1 id="pair-heading" class="m-0 text-[15px] font-[550] leading-6 text-ink">
          Pair this machine?
        </h1>
        <p class="m-0 mt-1 text-[12px] text-ink-2">
          Check that this code matches the one in the moil app.
        </p>
        <div
          class="mono mt-3 rounded-control border border-border bg-paper py-2 text-center text-[24px] leading-8 tracking-[0.12em] text-ink"
          data-testid="pairing-code"
        >
          {pending.code}
        </div>
        <dl class="m-0 mt-3 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1 text-[12px]">
          <dt class="text-ink-2">Name</dt>
          <dd class="m-0 truncate text-ink">{pending.name}</dd>
          <dt class="text-ink-2">System</dt>
          <dd class="m-0 truncate text-ink">{systemLabel(pending.os, pending.arch)}</dd>
          <dt class="text-ink-2">moil app</dt>
          <dd class="mono m-0 truncate text-ink">{pending.app_version}</dd>
        </dl>
      </div>
      <div class="border-t border-border p-4">
        <p class="m-0 text-[12px] text-ink-2">
          Pair only if you started this in the moil app on a computer you own. It will receive your
          recordings to transcribe them.
        </p>
        {#if error}<p class="m-0 mt-2 text-[12px] text-rec" role="alert">{error}</p>{/if}
        <div class="mt-4 flex justify-end gap-2">
          <button
            type="button"
            disabled={busy}
            class="inline-flex h-7 items-center rounded-control border border-border bg-paper px-2.5 text-[12px] text-ink transition-colors hover:bg-surface-2 disabled:opacity-60"
            onclick={() => void deny()}
          >
            Deny
          </button>
          <button
            type="button"
            disabled={busy}
            class="inline-flex h-7 items-center rounded-control border border-accent bg-accent px-3 text-[12px] font-[550] text-white transition-colors hover:border-accent-hover hover:bg-accent-hover disabled:opacity-60"
            onclick={() => void confirm()}
          >
            Pair
          </button>
        </div>
      </div>
    </section>
  {:else if view === 'expired' || view === 'denied'}
    <section class="mx-auto w-[min(100%,400px)] rounded-card border border-border bg-surface p-4">
      {#if view === 'expired'}
        <h1 class="m-0 text-[15px] font-[550] leading-6 text-ink">This code no longer works</h1>
        <p class="m-0 mt-1 text-[12px] text-ink-2">
          It expired or was already used. Start pairing again in the moil app.
        </p>
      {:else}
        <h1 class="m-0 text-[15px] font-[550] leading-6 text-ink">Machine not paired</h1>
        <p class="m-0 mt-1 text-[12px] text-ink-2">
          {deniedName || 'The machine'} was turned away and its code no longer works. If you didn't start
          this, you can close this tab.
        </p>
      {/if}
      <a class="mt-3 inline-block text-[12px] text-accent" href="/machines">Your machines</a>
    </section>
  {:else if data}
    <h1 class="m-0 text-[18px] font-[550] leading-6 text-ink">Machines</h1>
    <p class="m-0 mt-1.5 max-w-[560px] text-[12px] text-ink-2">
      Machines you pair transcribe your recordings on your own hardware. Your recordings only ever
      go to your own machines.
    </p>

    {#if paired}
      <div
        class="mt-4 flex items-start gap-2 rounded-card border border-ok/30 bg-ok/10 px-3 py-2 text-[12px] text-ink"
        role="status"
      >
        <CheckCircle
          class="mt-0.5 shrink-0 text-ok"
          size={16}
          weight="regular"
          aria-hidden="true"
        />
        <p class="m-0">
          Paired <strong class="font-[550]">{paired.name}</strong>.
          {paired.approved
            ? 'It will transcribe your new recordings.'
            : 'Approve the transcription bundle in its moil app to start transcribing.'}
        </p>
      </div>
    {/if}

    {#if error}<p class="m-0 mt-4 text-[12px] text-rec" role="alert">{error}</p>{/if}

    <section class="mt-8" aria-labelledby="machines-heading">
      <div class="flex items-center justify-between gap-4">
        <h2 id="machines-heading" class="m-0 text-[13px] font-[550] text-ink">Your machines</h2>
        <a
          class="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-control border border-accent bg-accent px-2.5 text-[12px] font-[550] text-white no-underline transition-colors hover:border-accent-hover hover:bg-accent-hover"
          href={pairHref}
          title="Opens the moil app on this computer"
        >
          <Plus size={16} weight="regular" aria-hidden="true" /> Add a machine
        </a>
      </div>

      {#if data.machines.length === 0}
        <p
          class="m-0 mt-2 rounded-card border border-border bg-surface px-3 py-6 text-center text-[12px] text-ink-2"
        >
          No machines yet. Add one to start transcribing your recordings.
        </p>
      {:else}
        <ul
          class="m-0 mt-2 list-none overflow-hidden rounded-card border border-border bg-surface p-0"
        >
          {#each data.machines as machine (machine.id)}
            <li
              class="flex items-start gap-3 border-b border-border px-3 py-2.5 last:border-b-0"
              data-machine-id={machine.id}
            >
              <span
                class="mt-[6px] size-2 shrink-0 rounded-full {stateDot(machine)}"
                aria-hidden="true"
              ></span>
              <div class="min-w-0 flex-1">
                <div class="truncate text-[13px] text-ink">{machine.name}</div>
                <div class="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[11px] text-ink-2">
                  <span data-testid="machine-state">{stateLabel(machine)}</span>
                  <span aria-hidden="true">·</span>
                  <span>{systemLabel(machine.os, machine.arch)}</span>
                  <span aria-hidden="true">·</span>
                  <span>moil <span class="mono">{machine.app_version}</span></span>
                </div>
                {#if !machine.approved}
                  <p
                    class="m-0 mt-2 flex items-start gap-1.5 rounded-control bg-surface-2 px-2 py-1.5 text-[12px] text-ink-2"
                    data-testid="approve-hint"
                  >
                    <Info
                      class="mt-0.5 shrink-0 text-warn"
                      size={16}
                      weight="regular"
                      aria-hidden="true"
                    />
                    <span>
                      Not transcribing yet. In the moil app on this machine, review and approve
                      <span class="mono text-ink">{data.bundle.name} {data.bundle.version}</span>
                      (<span class="mono text-ink" title={data.bundle.hash}
                        >{shortHash(data.bundle.hash)}</span
                      >).
                    </span>
                  </p>
                {/if}
              </div>
              <button
                type="button"
                disabled={busy}
                class="inline-flex h-6 shrink-0 items-center gap-1 rounded-control border px-2 text-[11px] transition-colors disabled:opacity-60 {unpairID ===
                machine.id
                  ? 'border-rec bg-rec text-white'
                  : 'border-border bg-paper text-ink-2 hover:bg-surface-2 hover:text-ink'}"
                aria-label={unpairID === machine.id
                  ? `Confirm unpair ${machine.name}`
                  : `Unpair ${machine.name}`}
                title={unpairID === machine.id ? undefined : 'Unpair'}
                onclick={() => void unpair(machine.id)}
              >
                {#if unpairID === machine.id}
                  Unpair?
                {:else}
                  <LinkBreak size={16} weight="regular" aria-hidden="true" />
                {/if}
              </button>
            </li>
          {/each}
        </ul>
      {/if}

      <div class="mt-3 text-[12px] text-ink-2">
        <label for="moil-address">Or paste this address into the moil app</label>
        <div class="mt-1.5 flex max-w-md items-center gap-1">
          <input
            id="moil-address"
            class="mono h-7 min-w-0 flex-1 rounded-control border border-border bg-paper px-2 text-[12px] text-ink outline-none focus:border-accent"
            value={data.moil_url}
            readonly
            onfocus={(event) => event.currentTarget.select()}
          />
          <button
            type="button"
            class="grid h-7 w-7 shrink-0 place-items-center rounded-control border border-border bg-paper p-0 text-ink-2 transition-colors hover:bg-surface-2 hover:text-ink"
            aria-label={copied ? 'Address copied' : 'Copy address'}
            title={copied ? 'Copied' : 'Copy address'}
            onclick={() => void copyAddress()}
          >
            {#if copied}
              <Check size={16} weight="regular" aria-hidden="true" />
            {:else}
              <Copy size={16} weight="regular" aria-hidden="true" />
            {/if}
          </button>
        </div>
      </div>
    </section>
  {/if}
</main>
