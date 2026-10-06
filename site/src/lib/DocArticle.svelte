<!-- A rendered docs page: its Markdown link, the prose, and "On this page". -->
<script lang="ts">
  import type { DocPage } from './docs.ts';
  import { markdownPath } from './docs.ts';

  let { page, html, toc }: { page: DocPage; html: string; toc: { id: string; title: string }[] } =
    $props();

  let article = $state<HTMLElement>();

  // Code blocks carry a Copy button; one listener serves them all.
  $effect(() => {
    const root = article;
    if (!root) return;
    const onClick = async (event: MouseEvent) => {
      const button = (event.target as HTMLElement).closest<HTMLButtonElement>('[data-copy]');
      const code = button?.closest('.code')?.querySelector('pre')?.textContent;
      if (!button || code === undefined) return;
      try {
        await navigator.clipboard.writeText(code);
        button.textContent = 'Copied';
        setTimeout(() => (button.textContent = 'Copy'), 1500);
      } catch {
        button.textContent = 'Select and copy';
      }
    };
    root.addEventListener('click', onClick);
    return () => root.removeEventListener('click', onClick);
  });
</script>

<svelte:head>
  <title>{page.slug === 'index' ? 'Deploy tide' : page.title} · tide</title>
  <meta name="description" content={page.summary} />
</svelte:head>

<div class="grid min-w-0 xl:grid-cols-[minmax(0,1fr)_13rem]">
  <div class="min-w-0 px-5 py-8 sm:px-10 sm:py-12">
    <div class="mb-6 flex justify-end text-[13px]">
      <a href={markdownPath(page)} data-sveltekit-reload class="text-ink-2 hover:text-ink"
        >View as Markdown</a
      >
    </div>
    <!-- eslint-disable-next-line svelte/no-at-html-tags -- our own Markdown, rendered at build time -->
    <article bind:this={article} class="prose">{@html html}</article>
  </div>
  {#if toc.length > 1}
    <aside class="hidden border-l border-line xl:block" aria-label="On this page">
      <div class="sticky top-0 px-5 py-12">
        <p class="text-[13px] text-ink-2">On this page</p>
        <ul class="mt-4 space-y-2 text-[13px]">
          {#each toc as item (item.id)}
            <li><a href="#{item.id}" class="text-ink-2 hover:text-ink">{item.title}</a></li>
          {/each}
        </ul>
      </div>
    </aside>
  {/if}
</div>
