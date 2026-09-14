<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import { GetClustersAccessStore, GetStreamPlacementContextStore } from "$houdini";
  import MediaPlacementEditor from "$lib/components/placement/MediaPlacementEditor.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { resolveOperationalStreamId } from "$lib/route-ids";

  const contextStore = new GetStreamPlacementContextStore();
  const clustersAccessStore = new GetClustersAccessStore();
  const streamId = $derived(resolveOperationalStreamId({ routeParamId: page.params.id ?? "" }));
  const source = $derived($contextStore.data?.stream ?? null);
  const sourceMode = $derived(source?.ingestMode ?? null);
  let loadError = $state("");

  function clusterName(clusterId: string) {
    return (
      $clustersAccessStore.data?.clustersAccess?.find((cluster) => cluster.clusterId === clusterId)
        ?.clusterName || clusterId
    );
  }

  onMount(async () => {
    try {
      const [contextResult] = await Promise.allSettled([
        contextStore.fetch({ variables: { id: page.params.id ?? "" } }),
        clustersAccessStore.fetch(),
      ]);
      if (contextResult.status === "rejected") throw contextResult.reason;
    } catch {
      loadError = "Could not load this stream's source mode.";
    }
  });
</script>

<svelte:head><title>Stream placement · FrameWorks</title></svelte:head>
<div class="h-full overflow-y-auto">
  <div class="mx-auto w-full max-w-5xl p-4 sm:p-6 lg:p-8">
    {#if streamId}
      <a
        class="inline-block mb-4 text-sm text-primary hover:underline"
        href={resolve("/streams/[id]", { id: streamId })}>← Back to stream</a
      >
      {#if loadError}
        <p role="alert" class="text-destructive">{loadError}</p>
      {:else if $contextStore.fetching || !sourceMode}
        <p role="status" class="text-sm text-muted-foreground">Loading stream routing…</p>
      {:else}
        {#if sourceMode === "PULL"}
          <div class="slab mb-4">
            <div class="slab-body--padded space-y-2">
              <div class="flex flex-wrap items-center gap-2">
                <Badge variant="outline">Pull source</Badge>
                <Badge variant={source?.pullSource?.enabled ? "default" : "secondary"}>
                  {source?.pullSource?.enabled ? "Enabled" : "Disabled"}
                </Badge>
                <span class="text-sm text-muted-foreground">
                  {source?.pullSource?.class ?? "public"} upstream
                </span>
              </div>
              <p class="text-sm text-muted-foreground">
                Source cluster pins belong to the pull source. The routing policy below controls
                where viewers are served.
              </p>
              {#if source?.pullSource?.allowedClusterIds.length}
                <div class="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <span>Source clusters:</span>
                  {#each source.pullSource.allowedClusterIds as clusterId (clusterId)}
                    <Badge variant="secondary" title={clusterId}>{clusterName(clusterId)}</Badge>
                  {/each}
                </div>
              {/if}
              <a
                class="text-sm text-primary hover:underline"
                href={resolve(`/streams/${page.params.id}`)}
              >
                Edit pull source in stream settings
              </a>
            </div>
          </div>
        {:else if sourceMode === "MANAGED"}
          <div class="slab mb-4">
            <div class="slab-body--padded space-y-2">
              <div class="flex flex-wrap items-center gap-2">
                <Badge variant="outline">Managed source</Badge>
                <Badge variant="secondary" class="uppercase">
                  {source?.managedSource?.sourceKind ?? "managed"}
                </Badge>
                {#if source?.managedSource?.alwaysOn}<Badge>Always on</Badge>{/if}
              </div>
              <p class="text-sm text-muted-foreground">
                The deployment owns source placement. The routing policy below controls where
                viewers are served and may relay this source to another cluster.
              </p>
              {#if source?.managedSource?.allowedClusterIds.length}
                <div class="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <span>Source clusters:</span>
                  {#each source.managedSource.allowedClusterIds as clusterId (clusterId)}
                    <Badge variant="secondary" title={clusterId}>{clusterName(clusterId)}</Badge>
                  {/each}
                </div>
              {/if}
            </div>
          </div>
        {/if}
        <MediaPlacementEditor
          scope={{ kind: "STREAM", streamId }}
          {sourceMode}
          initialVerb={page.url.searchParams.get("verb") === "ingest" ? "INGEST" : "SERVE"}
        />
      {/if}
    {:else}
      <p role="alert" class="text-destructive">
        A canonical stream ID is required to edit placement.
      </p>
    {/if}
  </div>
</div>
