<script lang="ts">
  import type { Track } from 'livekit-client';
  import { createMeter } from '$lib/rtc/audioLevel';

  let { track }: { track?: Track } = $props();

  // Uneven bar weights give the Apple-style silhouette; the rAF callback
  // writes transforms straight onto the elements — no Svelte state at 60fps.
  const weights = [0.55, 1, 0.8, 0.5];
  const idleScale = 0.18;

  const reduceMotion =
    typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  let bars = $state<HTMLSpanElement[]>([]);

  function apply(level: number): void {
    for (const [index, bar] of bars.entries()) {
      if (!bar) continue;
      const scale = idleScale + (1 - idleScale) * Math.min(1, level) * weights[index];
      bar.style.transform = `scaleY(${scale.toFixed(3)})`;
    }
  }

  $effect(() => {
    const mediaTrack = track?.mediaStreamTrack;
    if (!mediaTrack || reduceMotion) return;
    const dispose = createMeter(mediaTrack, apply);
    return () => {
      dispose();
      apply(0);
    };
  });
</script>

<div class="waveform" aria-hidden="true">
  {#each weights as weight, index (weight)}
    <span bind:this={bars[index]}></span>
  {/each}
</div>

<style>
  .waveform {
    position: absolute;
    right: 8px;
    bottom: 8px;
    display: flex;
    gap: 2px;
    align-items: center;
    padding: 5px;
    background: color-mix(in srgb, var(--stage) 78%, transparent);
    border: 1px solid var(--border-d);
    border-radius: var(--radius-control);
  }

  .waveform span {
    width: 2px;
    height: 10px;
    background: var(--text-2);
    border-radius: 999px;
    transform: scaleY(0.18);
    transform-origin: center;
    will-change: transform;
  }
</style>
