<script lang="ts">
  import '@fontsource-variable/geist';
  import '@fontsource-variable/geist-mono';
  import '../app.css';
  import { page } from '$app/state';
  import { siteURL } from '#lib/links.ts';

  let { children } = $props();
</script>

<!-- Link previews. Each page adds its own og:title and og:description. -->
<svelte:head>
  <meta property="og:type" content="website" />
  <meta property="og:site_name" content="tide" />
  <meta property="og:url" content="{siteURL}{page.url.pathname}" />
  <meta property="og:image" content="{siteURL}/og.png" />
  <meta property="og:image:width" content="1200" />
  <meta property="og:image:height" content="630" />
  <meta property="og:image:alt" content="A tide meeting with four people" />
  <meta name="twitter:card" content="summary_large_image" />
</svelte:head>

{@render children()}
