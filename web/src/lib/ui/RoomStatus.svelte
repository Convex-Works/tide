<script lang="ts">
  import type { RoomInfo } from '$lib/api/types.gen';
  import { compactAgo } from '$lib/format';

  let { room }: { room: RoomInfo } = $props();

  // A room someone is in gets a small chip in the stage's colors — the
  // meeting with the lights dimmed — whose dot turns red while it records.
  // An empty room only needs to say when it was last used.
  const live = $derived(room.active && room.num_participants > 0);
</script>

{#if live}
  <span class="inline-flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-ink-2">
    <span
      class="inline-flex shrink-0 items-center gap-1.5 rounded-full bg-stage px-2 text-[11px] leading-[18px] text-text"
      data-testid="room-live"
    >
      <span class="size-1.5 rounded-full {room.recording ? 'bg-rec' : 'bg-ok'}" aria-hidden="true"
      ></span>
      {room.recording ? 'Recording' : 'Live'}
    </span>
    <span>
      {room.num_participants}
      {room.num_participants === 1 ? 'person' : 'people'} in the room now
    </span>
  </span>
{:else}
  <span class="text-[12px] text-ink-2">
    {room.last_active_at != null
      ? `Last active ${compactAgo(room.last_active_at)}`
      : 'Not used yet'}
  </span>
{/if}
