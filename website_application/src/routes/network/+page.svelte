<script lang="ts">
  import NetworkObservations from "$lib/components/NetworkObservations.svelte";
  import { auth } from "$lib/stores/auth";
  let diagnostics = $state(false);
</script>

<svelte:head><title>Livepeer network | FrameWorks</title></svelte:head>
<div class="p-4 md:p-6">
  <h1 class="text-2xl font-semibold mb-4">Livepeer network</h1>
  {#if $auth.user?.platform_operator}
    <label class="flex items-center gap-2 mb-4"
      ><input type="checkbox" bind:checked={diagnostics} />Operator diagnostics</label
    >
  {/if}
  {#key diagnostics}<NetworkObservations {diagnostics} />{/key}
</div>
