<script lang="ts">
  import type { HairlineState } from '$lib/rtc/connection.svelte';

  let { state }: { state: HairlineState } = $props();
</script>

<div class="hairline {state}" data-testid="hairline" data-state={state} aria-hidden="true"></div>

<style>
  .hairline {
    position: fixed;
    z-index: 100;
    inset: 0 0 auto;
    height: 2px;
    background: var(--accent);
    transition: background var(--motion-fast);
  }

  .reconnecting {
    background: var(--warn);
  }

  .offline {
    background: var(--ink-2);
  }
</style>
