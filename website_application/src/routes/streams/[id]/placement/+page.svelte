<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import {
    GetClustersAccessStore,
    GetStreamPlacementContextStore,
    UpdateStreamStore,
  } from "$houdini";
  import MediaPlacementEditor from "$lib/components/placement/MediaPlacementEditor.svelte";
  import SourceLocationControl from "$lib/components/stream-details/SourceLocationControl.svelte";
  import SourceLocationSummary from "$lib/components/stream-details/SourceLocationSummary.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { resolveOperationalStreamId } from "$lib/route-ids";
  import { toast } from "$lib/stores/toast";
  import {
    draftFromSourceLocation,
    sameSourceLocation,
    sourceLocationInput,
    sourceLocationProblem,
    type SourceLocationDraft,
  } from "$lib/source-location";

  const contextStore = new GetStreamPlacementContextStore();
  const clustersAccessStore = new GetClustersAccessStore();
  const updateStream = new UpdateStreamStore();
  const streamId = $derived(resolveOperationalStreamId({ routeParamId: page.params.id ?? "" }));
  const source = $derived($contextStore.data?.stream ?? null);
  const sourceMode = $derived(source?.ingestMode ?? null);
  const sourceClass = $derived(source?.pullSource?.class === "private" ? "private" : "public");
  const clusters = $derived(
    ($clustersAccessStore.data?.clustersAccess ?? []).map((cluster) => ({
      clusterId: cluster.clusterId,
      clusterName: cluster.clusterName,
      accessLevel: cluster.accessLevel,
      allowPrivatePullSources: cluster.allowPrivatePullSources,
    }))
  );
  const savedLocation = $derived(draftFromSourceLocation(source?.sourceLocation));
  let loadError = $state("");
  let locationDraft = $state<SourceLocationDraft | null>(null);
  let savingLocation = $state(false);
  let editor = $state<ReturnType<typeof MediaPlacementEditor>>();
  let locationError = $state("");
  const locationProblem = $derived(
    locationDraft ? sourceLocationProblem(locationDraft, sourceClass, clusters) : null
  );
  const locationChanged = $derived(
    !!locationDraft && !!savedLocation && !sameSourceLocation(locationDraft, savedLocation)
  );

  function clusterName(clusterId: string) {
    return clusters.find((cluster) => cluster.clusterId === clusterId)?.clusterName || clusterId;
  }

  function editLocation() {
    if (!savedLocation) return;
    locationError = "";
    locationDraft = sourceLocationInput(savedLocation);
  }

  async function saveLocation() {
    if (!locationDraft || !locationChanged || locationProblem) return;
    savingLocation = true;
    locationError = "";
    try {
      const result = await updateStream.mutate({
        id: page.params.id ?? "",
        input: { sourceLocation: sourceLocationInput(locationDraft) },
      });
      const outcome = result.data?.updateStream;
      if (outcome?.__typename !== "Stream") {
        locationError =
          outcome && "message" in outcome && outcome.message
            ? outcome.message
            : "The source location was not saved.";
        return;
      }
      await contextStore.fetch({
        variables: { id: page.params.id ?? "" },
        policy: "NetworkOnly",
      });
      locationDraft = null;
      editor?.refreshAfterExternalChange();
      toast.success("Source location saved");
    } catch {
      locationError = "The source location was not saved. Try again.";
    } finally {
      savingLocation = false;
    }
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
        {#if sourceMode === "PULL" || sourceMode === "MANAGED"}
          <section class="slab mb-4" aria-labelledby="source-location-title">
            <div class="slab-header">
              <h2 id="source-location-title">Source location</h2>
            </div>
            <div class="slab-body--padded space-y-3">
              <div class="flex flex-wrap items-center gap-2">
                {#if sourceMode === "PULL"}
                  <Badge variant="outline">Pull source</Badge>
                  <Badge variant={source?.pullSource?.enabled ? "default" : "secondary"}>
                    {source?.pullSource?.enabled ? "Enabled" : "Disabled"}
                  </Badge>
                  <span class="text-sm text-muted-foreground">{sourceClass} upstream</span>
                {:else}
                  <Badge variant="outline">Managed source</Badge>
                  <Badge variant="secondary" class="uppercase">
                    {source?.managedSource?.sourceKind ?? "managed"}
                  </Badge>
                  {#if source?.managedSource?.alwaysOn}<Badge>Always on</Badge>{/if}
                {/if}
              </div>

              {#if locationDraft}
                <SourceLocationControl
                  value={locationDraft}
                  {clusters}
                  {sourceClass}
                  disabled={savingLocation}
                  onchange={(next) => (locationDraft = next)}
                />
                {#if locationError}
                  <p role="alert" class="text-sm text-destructive">{locationError}</p>
                {/if}
              {:else}
                <SourceLocationSummary location={source?.sourceLocation} {clusterName} />
                {#if sourceMode === "MANAGED"}
                  <p class="text-sm text-muted-foreground">
                    The deployment configuration sets where this source runs. The viewer rules below
                    control where viewers are served and may relay this source to another cluster.
                  </p>
                {:else if source?.sourceLocation.mode === "CUSTOM"}
                  <p class="text-sm text-muted-foreground">
                    Change these rules in the Source tab of the routing policy below.
                  </p>
                {:else}
                  <p class="text-sm text-muted-foreground">
                    This decides where FrameWorks opens the source. The viewer rules below control
                    where viewers are served.
                  </p>
                {/if}
              {/if}
            </div>
            {#if sourceMode === "PULL" && savedLocation}
              <div class="slab-actions slab-actions--row">
                {#if locationDraft}
                  <Button
                    variant="ghost"
                    disabled={savingLocation}
                    onclick={() => {
                      locationDraft = null;
                      locationError = "";
                    }}>Cancel</Button
                  >
                  <Button
                    variant="ghost"
                    disabled={savingLocation || !locationChanged || !!locationProblem}
                    onclick={saveLocation}
                    >{savingLocation ? "Saving…" : "Save source location"}</Button
                  >
                {:else}
                  <Button variant="ghost" onclick={editLocation}>Edit source location</Button>
                {/if}
              </div>
            {/if}
          </section>
        {/if}
        <MediaPlacementEditor
          bind:this={editor}
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
