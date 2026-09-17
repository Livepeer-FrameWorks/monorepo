<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";
  import type { SourceLocationValue } from "$lib/source-location";

  let {
    location,
    clusterName = (clusterId: string) => clusterId,
    placementHref,
  }: {
    location: SourceLocationValue | null | undefined;
    clusterName?: (clusterId: string) => string;
    placementHref?: string;
  } = $props();

  const mode = $derived(location?.mode ?? "ANY");
</script>

<div class="space-y-2 text-sm">
  {#if mode === "ANY"}
    <p>Any cluster FrameWorks chooses</p>
  {:else if mode === "RESTRICTED" && location}
    <div class="flex flex-wrap items-center gap-2">
      <span class="text-muted-foreground">Only these clusters:</span>
      {#each location.clusters as cluster (cluster.clusterId)}
        <Badge variant="outline" title={cluster.clusterId}>
          {clusterName(cluster.clusterId)}{cluster.nodeIds.length
            ? ` · ${cluster.nodeIds.length} ${cluster.nodeIds.length === 1 ? "node" : "nodes"}`
            : ""}
        </Badge>
      {/each}
    </div>
    {#if location.avoidNodeIds.length}
      <p class="text-xs text-muted-foreground">
        Avoids {location.avoidNodeIds.length}
        {location.avoidNodeIds.length === 1 ? "node" : "nodes"}.
      </p>
    {/if}
  {:else}
    <p>Custom placement rules</p>
    <p class="text-xs text-muted-foreground">
      This stream's source rules use conditions that this summary cannot show, such as regions or
      operators.
    </p>
    {#if placementHref}
      <!-- placementHref is built with resolve() by the page. -->
      <!-- eslint-disable svelte/no-navigation-without-resolve -->
      <a class="text-sm text-primary hover:underline" href={placementHref}>
        View and change them in the stream's placement rules
      </a>
      <!-- eslint-enable svelte/no-navigation-without-resolve -->
    {/if}
  {/if}
</div>
