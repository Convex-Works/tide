<script lang="ts">
  import type { Snippet } from 'svelte';
  import { page } from '$app/state';
  import { connectionChrome } from '$lib/rtc/connection.svelte';
  import Hairline from '$lib/ui/Hairline.svelte';
  import '@fontsource-variable/inter';
  import '@fontsource/geist-mono/400.css';
  import '../app.css';

  let { children }: { children: Snippet } = $props();
  let isEgressTemplate = $derived(page.url.pathname === '/egress-template');
</script>

<div class="app-shell">
  {#if !isEgressTemplate}<Hairline mode={connectionChrome.state} />{/if}
  {@render children()}
</div>
