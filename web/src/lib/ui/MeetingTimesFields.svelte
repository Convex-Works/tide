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
      aria-invalid={Boolean(error)}
      aria-describedby={error ? `${id}-error` : undefined}
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
      aria-invalid={Boolean(error)}
      aria-describedby={error ? `${id}-error` : undefined}
    />
  </label>
</div>
{#if error}
  <p id={`${id}-error`} class="m-0 mt-1 text-[11px] text-rec">{error}</p>
{/if}
