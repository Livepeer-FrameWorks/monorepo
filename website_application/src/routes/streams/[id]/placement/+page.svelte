<script lang="ts">
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import MediaPlacementEditor from "$lib/components/placement/MediaPlacementEditor.svelte";
  import { resolveOperationalStreamId } from "$lib/route-ids";
  const streamId = $derived(resolveOperationalStreamId({ routeParamId: page.params.id ?? "" }));
</script>

<svelte:head><title>Stream placement · FrameWorks</title></svelte:head>
{#if streamId}
  <a
    class="inline-block mb-4 text-sm text-primary hover:underline"
    href={resolve("/streams/[id]", { id: streamId })}>← Back to stream</a
  >
  <MediaPlacementEditor
    scope={{ kind: "STREAM", streamId }}
    initialVerb={page.url.searchParams.get("verb") === "ingest" ? "INGEST" : "SERVE"}
  />
{:else}
  <p role="alert" class="text-destructive">A canonical stream ID is required to edit placement.</p>
{/if}
