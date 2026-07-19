<script lang="ts">
  import type { Snippet } from 'svelte';
  import { connectionChrome } from '$lib/rtc/connection.svelte';
  import Hairline from '$lib/ui/Hairline.svelte';
  import '../app.css';

  let { children }: { children: Snippet } = $props();
</script>

<div class="app-shell">
  <Hairline state={connectionChrome.state} />
  {@render children()}
</div>
