<script lang="ts">
  import { goto } from '$app/navigation';
  import { Shuffle } from 'phosphor-svelte';
  import { ApiError, createRoom, errorMessage } from '$lib/api/client';
  import type { RoomInfo } from '$lib/api/types.gen';
  import { buildIcs, downloadIcs } from '$lib/ics';
  import { defaultTimes, resolveTimes, type MeetingTimes } from '$lib/schedule';
  import { normalizeSlug, slugError, slugMaxLength, suggestSlug } from '$lib/slug';
  import Button from './Button.svelte';
  import Dialog from './Dialog.svelte';
  import MeetingTimesFields from './MeetingTimesFields.svelte';

  // The dashboard's New dialog (ARCHITECTURE.md §5): an optional name, the
  // link prefilled with a suggestion in the server's format, and optional
  // times that turn into a downloaded .ics.
  let {
    open = $bindable(false),
    oncreated
  }: {
    open?: boolean;
    oncreated: (room: RoomInfo) => void;
  } = $props();

  // An untouched suggestion that collides is redrawn this many times before
  // the 409 is shown.
  const suggestionAttempts = 3;

  let name = $state('');
  let slugDraft = $state('');
  // True once the user typed in the link; a shuffle makes it a suggestion again.
  let slugEdited = $state(false);
  let slugInvalid = $state(false);
  let slugServerError = $state('');
  let withTimes = $state(false);
  let times = $state<MeetingTimes>(defaultTimes());
  let timesChecked = $state(false);
  let error = $state('');
  let creating = $state(false);
  let wasOpen = false;
  // Each opening is a session; a request answers only the session that sent
  // it, so a create that outlives its dialog can't close or steer the next.
  let session = 0;

  // The server allows 100 characters (code points). maxlength counts UTF-16
  // units, the same for any name outside the astral planes and stricter for
  // emoji, so the field never lets through a name the server refuses.
  const nameMaxLength = 100;

  const origin = typeof window === 'undefined' ? '' : window.location.origin;
  const slug = $derived(normalizeSlug(slugDraft));
  const slugMessage = $derived(slugServerError || (slugInvalid ? (slugError(slug) ?? '') : ''));

  // A cleared link is not an error: it asks the server to draw one.
  function linkError(value: string): boolean {
    return value !== '' && Boolean(slugError(value));
  }
  const resolved = $derived(resolveTimes(times));
  const timesError = $derived(
    withTimes && timesChecked && 'error' in resolved ? resolved.error : ''
  );

  $effect(() => {
    if (open && !wasOpen) reset();
    wasOpen = open;
  });

  function reset(): void {
    session++;
    name = '';
    slugDraft = suggestSlug();
    slugEdited = false;
    slugInvalid = false;
    slugServerError = '';
    withTimes = false;
    timesChecked = false;
    error = '';
    // A request still in flight keeps Create disabled until it settles.
  }

  function shuffle(): void {
    slugDraft = suggestSlug();
    slugEdited = false;
    slugInvalid = false;
    slugServerError = '';
  }

  function toggleTimes(event: Event & { currentTarget: HTMLInputElement }): void {
    withTimes = event.currentTarget.checked;
    // Next half hour from when the section is opened, not from page load.
    if (withTimes) {
      times = defaultTimes();
      timesChecked = false;
    }
  }

  async function submit(join: boolean): Promise<void> {
    if (creating) return;
    error = '';
    slugServerError = '';
    slugInvalid = linkError(slug);
    timesChecked = true;
    if (slugInvalid) return;
    const when = withTimes ? resolveTimes(times) : undefined;
    if (when && 'error' in when) return;

    creating = true;
    const mine = session;
    try {
      const room = await createWithRetry(mine);
      if (!room) return;
      // The room exists either way, so the list hears of it; the rest
      // belonged to a dialog that is gone.
      if (mine !== session) {
        oncreated(room);
        return;
      }
      if (when) {
        downloadIcs(
          `${room.slug}.ics`,
          buildIcs({
            slug: room.slug,
            name: room.name,
            url: `${origin}/m/${room.slug}`,
            host: window.location.host,
            start: when.start,
            end: when.end
          })
        );
      }
      oncreated(room);
      open = false;
      if (join) await goto(`/m/${room.slug}`);
    } finally {
      creating = false;
    }
  }

  // Creates the room, redrawing an untouched suggestion that is taken.
  // Returns undefined after showing why it failed. A blank link is left out,
  // and the server draws one.
  async function createWithRetry(mine: number): Promise<RoomInfo | undefined> {
    const trimmed = name.trim();
    for (let attempt = 1; ; attempt++) {
      try {
        return await createRoom({
          ...(trimmed ? { name: trimmed } : {}),
          ...(slug ? { slug } : {})
        });
      } catch (cause) {
        if (mine !== session) return undefined;
        const status = cause instanceof ApiError ? cause.status : 0;
        if (status === 409 && !slugEdited && attempt < suggestionAttempts) {
          slugDraft = suggestSlug();
          continue;
        }
        const message = errorMessage(cause, 'Could not create the room. Try again.');
        if (status === 409 || (status === 400 && /slug|link/i.test(message))) {
          slugServerError = message;
        } else {
          error = message;
        }
        return undefined;
      }
    }
  }
</script>

<Dialog bind:open title="New room" width={440} dismissible={!creating}>
  <form
    class="grid gap-3"
    onsubmit={(event) => {
      event.preventDefault();
      void submit(true);
    }}
    novalidate
  >
    <label class="grid gap-1" for="new-room-name">
      <span class="text-[12px] text-ink-2">Name</span>
      <input
        id="new-room-name"
        name="room-name"
        class="h-7 w-full min-w-0 rounded-control border border-border bg-surface px-2 text-[13px] text-ink outline-none placeholder:text-ink-2 focus:border-accent"
        bind:value={name}
        placeholder={slug || 'Same as the link'}
        maxlength={nameMaxLength}
        autocomplete="off"
        data-autofocus
      />
    </label>

    <div class="grid gap-1">
      <label class="text-[12px] text-ink-2" for="new-room-slug">Link</label>
      <div
        class="flex h-7 min-w-0 items-center rounded-control border bg-surface pl-2 focus-within:border-accent {slugMessage
          ? 'border-rec'
          : 'border-border'}"
      >
        <span class="mono min-w-0 shrink truncate text-[12px] text-ink-2" title={`${origin}/m/`}
          >{origin}/m/</span
        >
        <input
          id="new-room-slug"
          name="room-slug"
          class="slug-input mono h-full min-w-[8ch] flex-1 border-0 bg-transparent p-0 text-[12px] text-ink outline-none"
          value={slugDraft}
          placeholder="random"
          title="Leave blank for a random link"
          maxlength={slugMaxLength}
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          aria-invalid={Boolean(slugMessage)}
          aria-describedby={slugMessage ? 'new-room-slug-status' : undefined}
          oninput={(event) => {
            slugDraft = event.currentTarget.value.toLowerCase();
            slugEdited = true;
            slugServerError = '';
            // Once flagged, the rules stay until the link follows them.
            if (slugInvalid) slugInvalid = linkError(normalizeSlug(slugDraft));
          }}
          onblur={() => (slugInvalid = linkError(slug))}
        />
        <button
          type="button"
          class="grid h-full w-7 shrink-0 place-items-center rounded-r-control border-0 bg-transparent p-0 text-ink-2 transition-colors hover:bg-surface-2 hover:text-ink"
          aria-label="Suggest another link"
          title="Suggest another link"
          onclick={shuffle}
        >
          <Shuffle size={16} weight="regular" aria-hidden="true" />
        </button>
      </div>
      {#if slugMessage}
        <p id="new-room-slug-status" class="m-0 text-[11px] text-rec">{slugMessage}</p>
      {/if}
    </div>

    <div class="grid gap-2">
      <label class="flex w-fit cursor-pointer items-center gap-2 text-[13px] text-ink">
        <input
          type="checkbox"
          class="m-0 size-3.5 accent-[var(--accent)]"
          checked={withTimes}
          onchange={toggleTimes}
        />
        Add to calendar
      </label>
      {#if withTimes}
        <MeetingTimesFields bind:times error={timesError} />
      {/if}
    </div>

    {#if error}<p class="m-0 text-[12px] text-rec" role="alert">{error}</p>{/if}

    <div class="flex justify-end gap-2">
      <Button type="button" disabled={creating} onclick={() => void submit(false)}>Create</Button>
      <Button type="submit" variant="accent" disabled={creating}>Create &amp; join</Button>
    </div>
  </form>
</Dialog>

<style>
  /* The field's box takes the focus colour (focus-within) instead. */
  .slug-input:focus-visible {
    outline: none;
  }
</style>
