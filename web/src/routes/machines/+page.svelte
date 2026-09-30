<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from 'svelte';
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
    errorMessage,
    listMachines,
    me,
    pairing,
    removeMachine
  } from '$lib/api/client';
  import {
    AuthLoginPath,
    MachineBusy,
    MachineIdle,
    MachinePaused,
    type MachineInfo,
    type MachinesResponse,
    type PairingInfo
  } from '$lib/api/types.gen';
  import { compactAgo, osLabel, shortHash, systemLabel } from '$lib/format';

  // One route, two jobs (ARCHITECTURE.md §8.1): with ?code= the moil app sent
  // the host here to confirm a pairing; without one it lists their machines.
  type View = 'loading' | 'signed-out' | 'error' | 'confirm' | 'expired' | 'denied' | 'list';

  // Machines come online, go offline and get approved in the moil app while
  // this page is open, so the list refreshes itself quietly.
  const refreshIntervalMs = 10_000;

  const accentButton =
    'inline-flex h-7 shrink-0 items-center gap-1.5 rounded-control border border-accent bg-accent px-2.5 text-[12px] font-[550] text-white no-underline transition-colors hover:border-accent-hover hover:bg-accent-hover disabled:opacity-60';
  const plainButton =
    'inline-flex h-7 shrink-0 items-center gap-1.5 rounded-control border border-border bg-paper px-2.5 text-[12px] text-ink transition-colors hover:bg-surface-2 disabled:opacity-60';
  const textButton = 'border-0 bg-transparent p-0 text-[12px] text-accent';
  const field =
    'mono h-7 min-w-0 flex-1 rounded-control border border-border bg-paper px-2 text-[12px] text-ink outline-none focus:border-accent';

  let view = $state<View>('loading');
  let pending = $state<PairingInfo>();
  // The signed-in host's email, so the confirm card can say whose machine it becomes.
  let account = $state<string>();
  let data = $state<MachinesResponse>();
  // The machine this visit paired. The banner follows it in the live list.
  let paired = $state<Pick<MachineInfo, 'id' | 'name'>>();
  let deniedName = $state('');
  let error = $state('');
  // Read out by screen readers when an action removes what had focus.
  let announcement = $state('');
  let busy = $state(false);
  let unpairID = $state('');
  let copied = $state(false);
  let codeDraft = $state('');
  let copyTimer: ReturnType<typeof setTimeout> | undefined;
  let refreshTimer: ReturnType<typeof setInterval> | undefined;
  let refreshing = false;
  let destroyed = false;
  // Bumped whenever this page changes the list itself, so a background
  // refresh already in flight can't bring back a machine just unpaired.
  let generation = 0;
  // The code the current view was loaded for; plain, so writing it never
  // re-runs the effect below.
  let shownCode: string | undefined;
  // Set when the host typed a code, so the view it loads takes focus.
  let focusNextLoad = false;

  const code = $derived(page.url.searchParams.get('code')?.trim() ?? '');
  const signInHref = $derived(
    `${AuthLoginPath}?next=${encodeURIComponent(page.url.pathname + page.url.search)}`
  );
  const pairHref = $derived(data ? `moil://pair?url=${encodeURIComponent(data.moil_url)}` : '');
  const pairedMachine = $derived.by(() => {
    const id = paired?.id;
    return id ? data?.machines.find((machine) => machine.id === id) : undefined;
  });

  /** Moves focus to an element that replaced the control the host used. */
  async function focusOn(id: string): Promise<void> {
    await tick();
    document.getElementById(id)?.focus();
  }

  async function show(next: View): Promise<void> {
    view = next;
    await focusOn('view-focus');
  }

  async function load(current: string, focus = false): Promise<void> {
    view = 'loading';
    error = '';
    unpairID = '';
    if (current) paired = undefined;
    const stale = () => destroyed || current !== shownCode;
    let next: View;
    try {
      if (current) {
        // Who is signing in only adds a line; the pairing is what matters.
        const [waiting, host] = await Promise.all([pairing(current), me().catch(() => undefined)]);
        if (stale()) return;
        pending = waiting;
        account = host?.email;
        next = 'confirm';
      } else {
        const list = await listMachines();
        if (stale()) return;
        generation += 1;
        data = list;
        next = 'list';
      }
    } catch (cause) {
      if (stale()) return;
      if (cause instanceof AuthRequiredError) {
        next = 'signed-out';
      } else if (current && cause instanceof ApiError && cause.status === 404) {
        next = 'expired';
      } else {
        error = errorMessage(
          cause,
          current
            ? 'Could not look up the code. Check your connection, then try again.'
            : 'Could not load your machines. Check your connection, then try again.'
        );
        next = 'error';
      }
    }
    if (focus) await show(next);
    else view = next;
  }

  $effect(() => {
    const current = code;
    if (current === shownCode) return;
    shownCode = current;
    const focus = focusNextLoad;
    focusNextLoad = false;
    untrack(() => void load(current, focus));
  });

  function retry(): void {
    shownCode = code;
    void load(code, true);
  }

  /** Drops a spent code from the address, so a reload shows the list. */
  function leaveCode(): void {
    shownCode = '';
    void goto('/machines', { replaceState: true, keepFocus: true, noScroll: true });
  }

  function showList(): void {
    if (code) leaveCode();
    shownCode = '';
    void load('', true);
  }

  function enterCode(event: SubmitEvent): void {
    event.preventDefault();
    const entered = codeDraft.trim();
    if (!entered) return;
    codeDraft = '';
    if (entered === code) {
      // The address already has it (a retyped code): look it up again.
      void load(code, true);
      return;
    }
    focusNextLoad = true;
    void goto(`/machines?${new URLSearchParams({ code: entered })}`);
  }

  async function confirm(): Promise<void> {
    if (!pending || busy) return;
    const current = pending;
    busy = true;
    error = '';
    try {
      const machine = await confirmPairing(current.code);
      if (destroyed) return;
      paired = { id: machine.id, name: machine.name };
      pending = undefined;
      leaveCode();
      const list = await listMachines();
      if (destroyed) return;
      generation += 1;
      data = list;
      view = 'list';
      await focusOn('paired-banner');
    } catch (cause) {
      if (destroyed) return;
      if (cause instanceof AuthRequiredError) {
        await show('signed-out');
      } else if (paired) {
        // Paired, but the list didn't load: the error view says both, and
        // trying again loads the list under the paired banner.
        error = errorMessage(
          cause,
          "Your machines didn't load. Check your connection, then try again."
        );
        await show('error');
      } else if (cause instanceof ApiError && cause.status === 404) {
        await show('expired');
      } else {
        error = errorMessage(
          cause,
          'Could not pair the machine. Check your connection, then try again.'
        );
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
      if (destroyed) return;
      deniedName = current.name;
      pending = undefined;
      leaveCode();
      await show('denied');
    } catch (cause) {
      if (destroyed) return;
      if (cause instanceof AuthRequiredError) {
        await show('signed-out');
      } else if (cause instanceof ApiError && cause.status === 404) {
        await show('expired');
      } else {
        error = errorMessage(
          cause,
          'Could not deny the machine. Check your connection, then try again.'
        );
      }
    } finally {
      busy = false;
    }
  }

  async function unpair(machine: MachineInfo): Promise<void> {
    if (!data || busy) return;
    if (unpairID !== machine.id) {
      unpairID = machine.id;
      return;
    }
    busy = true;
    error = '';
    try {
      await removeMachine(machine.id).catch((cause: unknown) => {
        // Already gone (unpaired in another tab, say) is what the host asked for.
        if (!(cause instanceof ApiError && cause.status === 404)) throw cause;
      });
      if (destroyed) return;
      generation += 1;
      data.machines = data.machines.filter((item) => item.id !== machine.id);
      unpairID = '';
      announcement = `Unpaired ${machine.name}.`;
      await focusOn('view-focus');
    } catch (cause) {
      if (destroyed) return;
      if (cause instanceof AuthRequiredError) {
        await show('signed-out');
      } else {
        error = errorMessage(
          cause,
          'Could not unpair the machine. Check your connection, then try again.'
        );
      }
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
    if (refreshing || view !== 'list' || document.hidden) return;
    refreshing = true;
    const started = generation;
    try {
      const list = await listMachines();
      if (!destroyed && view === 'list' && started === generation) data = list;
    } catch (cause) {
      // A session that expired meanwhile needs a sign-in; anything else, the
      // next tick retries.
      if (!destroyed && view === 'list' && cause instanceof AuthRequiredError) view = 'signed-out';
    } finally {
      refreshing = false;
    }
  }

  function onVisibilityChange(): void {
    if (!document.hidden) void refresh();
  }

  function stateLabel(machine: MachineInfo): string {
    switch (machine.state) {
      case MachineIdle:
        return 'Online';
      case MachineBusy:
        return 'Busy';
      case MachinePaused:
        return 'Paused in moil';
    }
    const seen = machine.last_seen_at;
    return seen != null ? `Offline · last seen ${compactAgo(seen)}` : 'Never connected';
  }

  function stateDot(machine: MachineInfo): string {
    if (machine.state === MachineIdle || machine.state === MachineBusy) return 'bg-ok';
    if (machine.state === MachinePaused) return 'bg-warn';
    return 'border border-ink-2 bg-transparent';
  }

  onMount(() => {
    refreshTimer = setInterval(() => void refresh(), refreshIntervalMs);
    document.addEventListener('visibilitychange', onVisibilityChange);
  });
  onDestroy(() => {
    destroyed = true;
    if (copyTimer) clearTimeout(copyTimer);
    if (refreshTimer) clearInterval(refreshTimer);
    document.removeEventListener('visibilitychange', onVisibilityChange);
  });
</script>

<svelte:head>
  <title>Machines · klisi</title>
</svelte:head>

{#snippet codeForm(label: string)}
  <form class="text-[12px] text-ink-2" onsubmit={enterCode}>
    <label for="pairing-code-input">{label}</label>
    <div class="mt-1.5 flex items-center gap-1">
      <input
        id="pairing-code-input"
        class="{field} uppercase placeholder:text-ink-2/60"
        bind:value={codeDraft}
        placeholder="XXXX-XXXX"
        maxlength="16"
        autocomplete="off"
        autocapitalize="characters"
        spellcheck="false"
        required
      />
      <button type="submit" class={plainButton}>Continue</button>
    </div>
  </form>
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
  <p class="sr-only" role="status">{announcement}</p>

  {#if view === 'loading'}
    <p class="m-0 text-[12px] text-ink-2" aria-live="polite">Loading…</p>
  {:else if view === 'signed-out'}
    <section class="mx-auto w-[min(100%,400px)] rounded-card border border-border bg-surface p-4">
      <h1 id="view-focus" tabindex="-1" class="m-0 text-[15px] font-[550] leading-6 text-ink">
        {code ? 'Sign in to pair this machine' : 'Sign in to see your machines'}
      </h1>
      <p class="m-0 mt-1 text-[12px] text-ink-2">
        {code
          ? 'The machine will belong to the account you sign in with.'
          : 'Machines you pair with klisi transcribe your recordings.'}
      </p>
      <a class="{accentButton} mt-4" href={signInHref} data-sveltekit-reload>
        <SignIn size={16} weight="regular" aria-hidden="true" /> Sign in
      </a>
    </section>
  {:else if view === 'error'}
    <div class="rounded-card border border-border bg-surface px-4 py-8 text-center">
      {#if paired}
        <p class="m-0 mb-1 text-[13px] text-ink">Paired <bdi>{paired.name}</bdi>.</p>
      {/if}
      <p class="m-0 text-[13px] text-rec" role="alert">{error}</p>
      <button id="view-focus" class="{textButton} mt-2" type="button" onclick={retry}>
        Try again
      </button>
    </div>
  {:else if view === 'confirm' && pending}
    <section
      class="mx-auto w-[min(100%,400px)] rounded-card border border-border bg-surface"
      aria-labelledby="view-focus"
    >
      <div class="p-4">
        <h1 id="view-focus" tabindex="-1" class="m-0 text-[15px] font-[550] leading-6 text-ink">
          Pair this machine?
        </h1>
        <!-- One check, before anything else: the code alone proves nothing. -->
        <p class="m-0 mt-1 text-[13px] text-ink" data-testid="moil-address-check">
          Pair only if you just started pairing in the moil app on your own computer, and it shows
          this code and says it is pairing with
          <span class="mono [overflow-wrap:anywhere]">{pending.moil_url}</span>. Otherwise, choose
          Deny.
        </p>
        <div
          class="mono mt-3 rounded-control border border-border bg-paper py-1.5 text-center text-[18px] leading-7 tracking-[0.16em] text-ink"
          data-testid="pairing-code"
        >
          {pending.code}
        </div>
        <p id="machine-reported" class="m-0 mt-3 text-[11px] text-ink-2">Reported by the machine</p>
        <dl
          class="m-0 mt-1 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1 text-[12px]"
          aria-describedby="machine-reported"
        >
          <dt class="text-ink-2">Name</dt>
          <dd class="m-0 text-ink [overflow-wrap:anywhere]"><bdi>{pending.name}</bdi></dd>
          <dt class="text-ink-2">System</dt>
          <dd class="m-0 text-ink [overflow-wrap:anywhere]">
            <bdi>{systemLabel(pending.os, pending.arch)}</bdi>
          </dd>
          <dt class="text-ink-2">moil app</dt>
          <dd class="mono m-0 text-ink [overflow-wrap:anywhere]">
            <bdi>{pending.app_version}</bdi>
          </dd>
        </dl>
      </div>
      <div class="border-t border-border p-4">
        <p class="m-0 text-[12px] text-ink">
          Once paired, it downloads and transcribes every new recording of rooms you own, until you
          unpair it here.
        </p>
        {#if account}
          <p class="m-0 mt-1 text-[12px] text-ink-2">
            Pairs with your account <span class="text-ink [overflow-wrap:anywhere]">{account}</span
            >.
          </p>
        {/if}
        {#if error}<p class="m-0 mt-2 text-[12px] text-rec" role="alert">{error}</p>{/if}
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" disabled={busy} class={plainButton} onclick={() => void deny()}>
            Deny
          </button>
          <button
            type="button"
            disabled={busy}
            class="{accentButton} px-3"
            onclick={() => void confirm()}
          >
            Pair
          </button>
        </div>
      </div>
    </section>
  {:else if view === 'expired'}
    <section class="mx-auto w-[min(100%,400px)] rounded-card border border-border bg-surface p-4">
      <h1 id="view-focus" tabindex="-1" class="m-0 text-[15px] font-[550] leading-6 text-ink">
        This code doesn't work
      </h1>
      <p class="m-0 mt-1 text-[12px] text-ink-2">
        It expired, was already used, or was mistyped. Enter it again, or start pairing again in the
        moil app.
      </p>
      <div class="mt-3">{@render codeForm('Pairing code')}</div>
      <button type="button" class="{textButton} mt-3" onclick={showList}>Your machines</button>
    </section>
  {:else if view === 'denied'}
    <section class="mx-auto w-[min(100%,400px)] rounded-card border border-border bg-surface p-4">
      <h1 id="view-focus" tabindex="-1" class="m-0 text-[15px] font-[550] leading-6 text-ink">
        Machine not paired
      </h1>
      <p class="m-0 mt-1 text-[12px] text-ink-2">
        You denied <bdi class="text-ink">{deniedName}</bdi>. Nothing was shared with it, and its
        code no longer works.
      </p>
      <button type="button" class="{textButton} mt-3" onclick={showList}>Your machines</button>
    </section>
  {:else if view === 'list' && data}
    <div class="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
      <div class="min-w-0">
        <h1 id="view-focus" tabindex="-1" class="m-0 text-[18px] font-[550] leading-6 text-ink">
          Machines
        </h1>
        <p class="m-0 mt-1.5 max-w-[560px] text-[12px] text-ink-2">
          Machines you pair transcribe your recordings on your own hardware. Your recordings only
          ever go to your own machines.
        </p>
      </div>
      <a class={accentButton} href={pairHref} title="Opens the moil app on this computer">
        <Plus size={16} weight="regular" aria-hidden="true" /> Add a machine
      </a>
    </div>

    {#if pairedMachine}
      <div
        id="paired-banner"
        tabindex="-1"
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
          Paired <strong class="font-[550]"><bdi>{pairedMachine.name}</bdi></strong>.
          {pairedMachine.approved
            ? 'It will transcribe every new recording of rooms you own.'
            : `To start transcribing, review and approve the ${data.bundle.name} bundle in its moil app.`}
        </p>
      </div>
    {/if}

    {#if error}<p class="m-0 mt-4 text-[12px] text-rec" role="alert">{error}</p>{/if}

    <section class="mt-6" aria-labelledby="view-focus">
      {#if data.machines.length === 0}
        <!-- Onboarding: most hosts have never heard of moil. -->
        <div
          class="rounded-card border border-border bg-surface px-3 py-3 text-[12px] text-ink"
          data-testid="onboarding"
        >
          <p class="m-0 text-[13px] font-[550]">Transcribe on a computer of your own</p>
          <ol class="m-0 mt-2 grid list-none gap-2 p-0">
            <li class="flex gap-2">
              <span class="mono w-4 shrink-0 text-ink-2" aria-hidden="true">1</span>
              <span>
                Get the moil app on the computer that will transcribe.
                <a class="text-accent" href={data.app_url} target="_blank" rel="noreferrer"
                  >Download moil</a
                >
              </span>
            </li>
            <li class="flex gap-2">
              <span class="mono w-4 shrink-0 text-ink-2" aria-hidden="true">2</span>
              <span>
                Pair it with klisi: choose <strong class="font-[550]">Add a machine</strong> on that computer,
                or paste the address below into its moil app.
              </span>
            </li>
            <li class="flex gap-2">
              <span class="mono w-4 shrink-0 text-ink-2" aria-hidden="true">3</span>
              <span>
                In moil, review and approve the {data.bundle.name} bundle, version
                <span class="mono">{data.bundle.version}</span>
                (<span class="mono" title={data.bundle.hash}>{shortHash(data.bundle.hash)}</span>).
              </span>
            </li>
          </ol>
        </div>
      {:else}
        <ul class="m-0 list-none overflow-hidden rounded-card border border-border bg-surface p-0">
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
                <div class="text-[13px] text-ink [overflow-wrap:anywhere]">
                  <bdi>{machine.name}</bdi>
                </div>
                <div class="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[11px] text-ink-2">
                  <span data-testid="machine-state">{stateLabel(machine)}</span>
                  <span aria-hidden="true">·</span>
                  <span><bdi>{osLabel(machine.os)}</bdi></span>
                  <span aria-hidden="true">·</span>
                  <span>moil <bdi class="mono">{machine.app_version}</bdi></span>
                </div>
                {#if !machine.approved}
                  <p
                    class="m-0 mt-2 flex items-start gap-1.5 rounded-control bg-surface-2 px-2 py-1.5 text-[12px] text-ink"
                    data-testid="approve-hint"
                  >
                    <Info
                      class="mt-0.5 shrink-0 text-warn"
                      size={16}
                      weight="regular"
                      aria-hidden="true"
                    />
                    <span>
                      Not transcribing yet. In the moil app on this machine, review and approve the
                      {data.bundle.name} bundle, version
                      <span class="mono">{data.bundle.version}</span>
                      (<span class="mono" title={data.bundle.hash}
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
                onclick={() => void unpair(machine)}
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
    </section>

    <!-- A computer that isn't this one: the deep link above can't reach it,
         so its moil app starts the pairing and shows the code. -->
    <section class="mt-8" aria-labelledby="pair-another-heading">
      <div class="flex flex-wrap items-baseline justify-between gap-x-4">
        <h2 id="pair-another-heading" class="m-0 text-[13px] font-[550] text-ink">
          Pair another computer
        </h2>
        {#if data.machines.length > 0}
          <!-- Without a machine yet, the steps above link it. -->
          <a class="text-[12px] text-accent" href={data.app_url} target="_blank" rel="noreferrer"
            >Get moil</a
          >
        {/if}
      </div>
      <ol class="m-0 mt-2 grid list-none gap-4 p-0 sm:grid-cols-2">
        <li class="flex min-w-0 gap-2 text-[12px]">
          <span class="mono w-4 shrink-0 text-ink-2" aria-hidden="true">1</span>
          <div class="min-w-0 flex-1 text-ink-2">
            <label for="moil-address">Paste this address into its moil app</label>
            <div class="mt-1.5 flex items-center gap-1">
              <input
                id="moil-address"
                class={field}
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
        </li>
        <li class="flex min-w-0 gap-2 text-[12px]">
          <span class="mono w-4 shrink-0 text-ink-2" aria-hidden="true">2</span>
          <div class="min-w-0 flex-1">{@render codeForm('Enter the code it shows')}</div>
        </li>
      </ol>
    </section>
  {/if}
</main>
