<script lang="ts">
  import type { HairlineState } from '#lib/rtc/connection.svelte.js';

  let { mode, agent = false }: { mode: HairlineState; agent?: boolean } = $props();
</script>

<div class="hairline {mode}" data-testid="hairline" data-state={mode} aria-hidden="true"></div>
<!-- Its own line, so that no other state hides it: alone it takes the whole
     width, and beside another state the right half. -->
{#if agent}
  <div
    class="hairline agent"
    class:shared={mode !== 'connected'}
    data-testid="hairline-agent"
    aria-hidden="true"
  ></div>
{/if}

<style>
  .hairline {
    position: fixed;
    z-index: 100;
    inset: 0 0 auto;
    height: 2px;
    background: var(--accent);
    transition: background var(--motion-fast);
  }

  .agent {
    background: var(--ok);
  }

  .agent.shared {
    left: 50%;
  }

  .reconnecting {
    background: var(--warn);
  }

  .offline {
    background: var(--ink-2);
  }

  .recording {
    background: var(--rec);
    animation: recording-pulse 2s ease-in-out infinite;
  }

  @keyframes recording-pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.45;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .recording {
      animation: none;
    }
  }
</style>
