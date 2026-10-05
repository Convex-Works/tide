<script lang="ts">
  import { onMount } from 'svelte';

  // HH:MM, with the viewer's locale deciding the hour cycle (23:03 or
  // 11:03 PM). 'numeric' hours would show midnight as 0:00 in en-GB.
  const format = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
  const pad = (value: number) => String(value).padStart(2, '0');

  let now = $state(new Date());

  onMount(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    // One wake-up per minute, on the boundary. The delay is measured afresh
    // each time, so a late or early timer corrects itself on the next tick.
    const tick = () => {
      clearTimeout(timer);
      now = new Date();
      timer = setTimeout(tick, 60_000 - (now.getTime() % 60_000));
    };
    // Background tabs may hold timers back; catch up when the tab returns.
    const onVisibility = () => {
      if (document.visibilityState === 'visible') tick();
    };
    tick();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      clearTimeout(timer);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  });
</script>

<time
  class="clock"
  data-testid="stage-clock"
  datetime={`${pad(now.getHours())}:${pad(now.getMinutes())}`}>{format.format(now)}</time
>

<style>
  .clock {
    flex: none;
    color: var(--text);
    font-size: 12.5px;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
</style>
