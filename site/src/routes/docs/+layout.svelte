<script lang="ts">
  import { page } from '$app/state';
  import SiteFooter from '#lib/SiteFooter.svelte';
  import SiteHeader from '#lib/SiteHeader.svelte';
  import { docPages, type DocPage } from '#lib/docs.ts';

  let { children } = $props();

  const guide = docPages.filter((doc) => doc.group === 'guide');
  const reference = docPages.filter((doc) => doc.group === 'reference');
  let menu = $state<HTMLDetailsElement>();

  // Following a link in the phone menu closes it.
  $effect(() => {
    void page.url.pathname;
    if (menu) menu.open = false;
  });
</script>

{#snippet item(doc: DocPage)}
  <li>
    <a
      href={doc.path}
      aria-current={page.url.pathname === doc.path ? 'page' : undefined}
      class="block border-l border-transparent py-1.5 pr-3 pl-4 text-[14px] text-ink-2 transition-colors hover:text-ink aria-[current=page]:border-tide aria-[current=page]:bg-surface aria-[current=page]:text-ink"
    >
      {doc.title}
    </a>
  </li>
{/snippet}

{#snippet contents()}
  <ol class="py-3">
    {#each guide as doc (doc.slug)}{@render item(doc)}{/each}
  </ol>
  <p class="mt-4 px-4 text-[13px] text-ink-2">Reference</p>
  <ul class="py-3">
    {#each reference as doc (doc.slug)}{@render item(doc)}{/each}
  </ul>
{/snippet}

<div class="flex min-h-dvh flex-col">
  <SiteHeader />
  <div class="flex flex-1 flex-col px-4 sm:px-6">
    <div class="grid flex-1 border border-line lg:grid-cols-[15rem_minmax(0,1fr)]">
      <details bind:this={menu} class="group border-b border-line lg:hidden">
        <summary
          class="flex cursor-pointer list-none items-center justify-between px-4 py-3 text-[14px] text-ink-2 [&::-webkit-details-marker]:hidden"
        >
          Contents <span class="group-open:hidden">+</span><span class="hidden group-open:inline"
            >−</span
          >
        </summary>
        <nav aria-label="Documentation" class="border-t border-line">{@render contents()}</nav>
      </details>
      <nav aria-label="Documentation" class="hidden border-r border-line lg:block">
        <div class="sticky top-0">{@render contents()}</div>
      </nav>
      <main class="min-w-0">{@render children()}</main>
    </div>
  </div>
  <SiteFooter />
</div>
