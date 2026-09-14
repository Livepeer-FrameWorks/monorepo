<script lang="ts">
  import MediaPlacementEditor from "$lib/components/placement/MediaPlacementEditor.svelte";
</script>

<svelte:head><title>Media placement · FrameWorks</title></svelte:head>
<div class="h-full overflow-y-auto">
  <div class="mx-auto w-full max-w-5xl p-4 sm:p-6 lg:p-8">
    <MediaPlacementEditor scope={{ kind: "TENANT" }} />
  </div>
</div>
