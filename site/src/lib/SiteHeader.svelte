<script lang="ts">
  import { resolve } from '$app/paths';
  import { page } from '$app/state';
  import GithubLogo from 'phosphor-svelte/lib/GithubLogo';
  import { repositoryURL } from './links.ts';
  import Logo from './Logo.svelte';

  const configuration = resolve('/docs/[slug]', { slug: 'configuration' });
  const links = [
    {
      href: resolve('/docs'),
      title: 'Docs',
      current: (path: string) => path.startsWith('/docs') && path !== configuration
    },
    {
      href: configuration,
      title: 'Configuration',
      current: (path: string) => path === configuration
    },
    { href: '/llms.txt', title: 'llms.txt', current: () => false, reload: true }
  ];
</script>

<header class="flex h-16 shrink-0 items-center gap-8 px-4 sm:px-6">
  <a href={resolve('/')} class="text-ink" aria-label="tide home">
    <Logo size={22} tone="mono" wordmark />
  </a>
  <nav aria-label="Site">
    <ul class="flex items-center gap-5 text-[14px]">
      {#each links as link (link.href)}
        <li class={link.reload ? 'hidden sm:block' : ''}>
          <a
            href={link.href}
            data-sveltekit-reload={link.reload ? '' : undefined}
            aria-current={link.current(page.url.pathname) ? 'page' : undefined}
            class="text-ink-2 transition-colors hover:text-ink aria-[current=page]:text-ink"
            >{link.title}</a
          >
        </li>
      {/each}
    </ul>
  </nav>
  <div class="ml-auto flex items-center gap-4">
    <a
      href={repositoryURL}
      class="flex items-center gap-1.5 text-[14px] text-ink-2 transition-colors hover:text-ink"
      title="tide on GitHub"
    >
      <GithubLogo size={18} />Source
    </a>
    <a class="button button-primary" href={resolve('/docs')}>Deploy</a>
  </div>
</header>
