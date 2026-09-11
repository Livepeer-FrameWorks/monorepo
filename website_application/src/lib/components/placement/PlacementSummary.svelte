<script lang="ts">
  import { resolve } from "$app/paths";
  import type { Verb } from "$lib/placement/model";
  let { streamId, verb }: { streamId: string; verb: Verb } = $props();
</script>

<div class="slab">
  <div class="slab-body--padded flex flex-wrap items-center justify-between gap-3">
    <div>
      <h3 class="text-sm font-medium">
        {verb === "INGEST" ? "Ingest" : "Viewer delivery"} placement
      </h3>
      <p class="text-sm text-muted-foreground">
        Inspect inherited account restrictions, preview destinations or customize this stream.
      </p>
    </div>
    <a
      class="text-sm text-primary hover:underline"
      href={resolve(
        `/streams/${streamId}/placement?verb=${verb === "INGEST" ? "ingest" : "serve"}`
      )}>Review stream placement →</a
    >
  </div>
</div>
