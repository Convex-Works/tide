<script lang="ts">
  import type { RoomInfo } from '$lib/api/types.gen';
  import { buildIcs, downloadIcs } from '$lib/ics';
  import { defaultTimes, resolveTimes, type MeetingTimes } from '$lib/schedule';
  import Button from './Button.svelte';
  import Dialog from './Dialog.svelte';
  import MeetingTimesFields from './MeetingTimesFields.svelte';

  // Add to calendar for an existing room (rooms are reusable): pick a time,
  // download <slug>.ics. Nothing reaches the server.
  let {
    room,
    open = $bindable(false)
  }: {
    room: RoomInfo | undefined;
    open?: boolean;
  } = $props();

  let times = $state<MeetingTimes>(defaultTimes());
  let checked = $state(false);
  let wasOpen = false;

  const resolved = $derived(resolveTimes(times));
  const error = $derived(checked && 'error' in resolved ? resolved.error : '');

  // Each opening starts from the next half hour.
  $effect(() => {
    if (open && !wasOpen) {
      times = defaultTimes();
      checked = false;
    }
    wasOpen = open;
  });

  function download(event: SubmitEvent): void {
    event.preventDefault();
    checked = true;
    if (!room || 'error' in resolved) return;
    const origin = window.location.origin;
    downloadIcs(
      `${room.slug}.ics`,
      buildIcs({
        slug: room.slug,
        name: room.name,
        url: `${origin}/m/${room.slug}`,
        host: window.location.host,
        start: resolved.start,
        end: resolved.end
      })
    );
    open = false;
  }
</script>

<Dialog bind:open title="Add to calendar" width={400}>
  <form class="grid gap-3" onsubmit={download} novalidate>
    <p class="m-0 -mt-2 truncate text-[12px] text-ink-2">{room?.name}</p>
    <div>
      <MeetingTimesFields bind:times {error} autofocus />
    </div>
    <div class="flex justify-end">
      <Button type="submit" variant="accent">Download .ics</Button>
    </div>
  </form>
</Dialog>
