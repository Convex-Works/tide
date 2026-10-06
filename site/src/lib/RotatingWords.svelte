<script lang="ts">
  import { onMount } from 'svelte';

  // Every word is in the prerendered HTML, so search engines read them all.
  // Screen readers read only the first; the rest are decoration.
  let { words, interval = 2600 }: { words: string[]; interval?: number } = $props();

  let index = $state(0);
  const previous = $derived((index - 1 + words.length) % words.length);

  onMount(() => {
    if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    const timer = setInterval(() => (index = (index + 1) % words.length), interval);
    return () => clearInterval(timer);
  });
</script>

<span class="words">
  {#each words as word, i (word)}
    <span
      class="word"
      class:current={i === index}
      class:previous={i === previous}
      aria-hidden={i === 0 ? undefined : 'true'}>{word}</span
    >
  {/each}
</span>

<style>
  /* The words share one grid cell, so the line keeps the width of the longest. */
  .words {
    display: inline-grid;
  }

  .word {
    grid-area: 1 / 1;
    white-space: nowrap;
    opacity: 0;
    transform: translateY(0.35em);
    transition:
      opacity 500ms ease,
      transform 500ms ease;
  }

  .word.previous {
    transform: translateY(-0.35em);
  }

  .word.current {
    opacity: 1;
    transform: none;
  }
</style>
