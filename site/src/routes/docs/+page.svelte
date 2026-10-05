<script lang="ts">
  import DocArticle from '#lib/DocArticle.svelte';

  let { data } = $props();
</script>

<DocArticle {...data} />
