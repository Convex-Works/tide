<script lang="ts">
  import type { MeetingTimes } from '$lib/schedule';

  // Date, start and end of a meeting in the viewer's local time, for a
  // calendar invite. Shared by the New dialog and a card's Add to calendar.
  let {
    times = $bindable(),
    error = '',
    autofocus = false
  }: {
    times: MeetingTimes;
    /** Shown under the fields, e.g. an end before the start. */
    error?: string;
    /** Marks the date field as the dialog's first field. */
    autofocus?: boolean;
  } = $props();

  const id = $props.id();
  const errorId = `${id}-error`;

  // Which fields the error is about: the missing ones, or both times when
  // all are there and the end comes too early.
  const dateMissing = $derived(!/^\d{4}-\d{2}-\d{2}$/.test(times.date));
  const startMissing = $derived(!/^\d{2}:\d{2}$/.test(times.start));
  const endMissing = $derived(!/^\d{2}:\d{2}$/.test(times.end));
  const anyMissing = $derived(dateMissing || startMissing || endMissing);
  const dateInvalid = $derived(Boolean(error) && dateMissing);
  const startInvalid = $derived(Boolean(error) && (!anyMissing || startMissing));
  const endInvalid = $derived(Boolean(error) && (!anyMissing || endMissing));
  const field =
    'h-7 w-full min-w-0 rounded-control border border-border bg-surface px-2 text-[12px] text-ink outline-none focus:border-accent aria-[invalid=true]:border-rec';
</script>

<div class="grid grid-cols-2 gap-2 sm:grid-cols-[minmax(0,1fr)_112px_112px]">
  <label class="col-span-2 grid gap-1 sm:col-span-1" for={`${id}-date`}>
    <span class="text-[12px] text-ink-2">Date</span>
    <input
      id={`${id}-date`}
      class={field}
      type="date"
      bind:value={times.date}
      required
      aria-invalid={dateInvalid}
      aria-describedby={dateInvalid ? errorId : undefined}
      data-autofocus={autofocus || undefined}
    />
  </label>
  <label class="grid gap-1" for={`${id}-start`}>
    <span class="text-[12px] text-ink-2">Starts</span>
    <input
      id={`${id}-start`}
      class={field}
      type="time"
      bind:value={times.start}
      required
      aria-invalid={startInvalid}
      aria-describedby={startInvalid ? errorId : undefined}
    />
  </label>
  <label class="grid gap-1" for={`${id}-end`}>
    <span class="text-[12px] text-ink-2">Ends</span>
    <input
      id={`${id}-end`}
      class={field}
      type="time"
      bind:value={times.end}
      required
      aria-invalid={endInvalid}
      aria-describedby={endInvalid ? errorId : undefined}
    />
  </label>
</div>
{#if error}
  <p id={errorId} class="m-0 mt-1 text-[11px] text-rec">{error}</p>
{/if}
