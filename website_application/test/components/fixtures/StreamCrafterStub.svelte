<script lang="ts">
  import { onMount } from "svelte";
  import { studioMounts, studioProps } from "./streamcrafter-stub";

  let props: Record<string, unknown> = $props();

  $effect(() => {
    studioProps.current = props;
  });

  onMount(() => {
    studioMounts.count++;
  });
</script>

<div data-testid="studio">studio</div>
