<script lang="ts">
  let { query = $bindable(), variables = $bindable() } = $props();
</script>

<textarea aria-label="Query" bind:value={query}></textarea>
<textarea aria-label="Variables" bind:value={variables}></textarea>
