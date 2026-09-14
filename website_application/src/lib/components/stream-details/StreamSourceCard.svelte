<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";

  interface Stream {
    ingestMode?: "PUSH" | "PULL" | "MANAGED" | string | null;
    pullSource?: { enabled: boolean; class: string } | null;
    managedSource?: { sourceKind: string; alwaysOn: boolean; allowedClusterIds: string[] } | null;
  }

  let { stream }: { stream: Stream } = $props();

  const title = $derived(stream.ingestMode === "PULL" ? "Pull Source" : "Managed Source");
  const description = $derived(
    stream.ingestMode === "PULL"
      ? "FrameWorks opens the configured upstream when playback needs it."
      : "The deployment supplies this stream from a local file, playlist, or process."
  );
</script>

<div class="slab h-full shadow-none border-none">
  <div class="slab-header">
    <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">{title}</h3>
  </div>
  <div class="slab-body--padded space-y-3">
    <div class="flex flex-wrap gap-2">
      {#if stream.ingestMode === "PULL"}
        <Badge variant="outline" class="uppercase">{stream.pullSource?.class ?? "pull"}</Badge>
        <Badge variant={stream.pullSource?.enabled ? "default" : "secondary"}>
          {stream.pullSource?.enabled ? "Enabled" : "Disabled"}
        </Badge>
      {:else}
        <Badge variant="outline" class="uppercase">
          {stream.managedSource?.sourceKind ?? "managed"}
        </Badge>
        <Badge variant={stream.managedSource?.alwaysOn ? "default" : "secondary"}>
          {stream.managedSource?.alwaysOn ? "Always on" : "On demand"}
        </Badge>
      {/if}
    </div>
    <p class="text-sm text-muted-foreground">{description}</p>
  </div>
</div>
