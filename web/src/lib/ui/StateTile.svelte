<script lang="ts">
  import { Users } from 'phosphor-svelte';
  import type { RoomInfo } from '$lib/api/types.gen';
  import { compactAgo } from '$lib/format';

  let { room }: { room: RoomInfo } = $props();

  // A generated preview of the room's current state — never a real video frame
  // (privacy + weight). Dark "stage" ground so the card reads as the meeting
  // with the lights dimmed, matching the in-call surface.
  const live = $derived(room.active && room.num_participants > 0);
</script>

<div
  class="grid aspect-[12/10] w-full select-none place-items-center rounded-tile bg-stone-200 px-2 text-center"
>
  {#if live}
    <div class="flex flex-col items-center gap-1.5">
      <span
        class="inline-flex items-center gap-1.5 rounded-full bg-red-500/20 px-3 py-0.5 text-[11px] text-red-500"
      >
        {#if room.recording}
          <span class="size-1.5 rounded-full bg-red-500 animate-pulse" aria-hidden="true"></span> Recording
        {:else}
          <span class="size-1.5 rounded-full bg-ok" aria-hidden="true"></span> Live
        {/if}
      </span>
      <span class="inline-flex items-center gap-1 text-[11px] text-text-2">
        <Users size={12} weight="regular" aria-hidden="true" />
        {room.num_participants}
      </span>
    </div>
  {:else if room.last_active_at != null}
    <div class="flex flex-col items-center gap-1.5">
      <span
        class="inline-flex items-center gap-1.5 rounded-full bg-stone-300 px-4 py-0.5 text-[11px] text-text-2"
      >
        <span class="size-1.5 rounded-full bg-text-2" aria-hidden="true"></span> Idle
      </span>
      <!-- <span class="text-[11px] text-text-2">{compactAgo(room.last_active_at)}</span> -->
    </div>
  {:else}
    <span
      class="inline-flex items-center rounded-full bg-surface px-4 py-0.5 text-[11px] text-text-2"
    >
      New
    </span>
  {/if}
</div>
