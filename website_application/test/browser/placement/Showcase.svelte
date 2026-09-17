<script lang="ts">
  import MediaCapacityConsentEditor from "$lib/components/placement/MediaCapacityConsentEditor.svelte";
  import MediaPlacementEditor from "$lib/components/placement/MediaPlacementEditor.svelte";
  import SourceLocationControl from "$lib/components/stream-details/SourceLocationControl.svelte";
  import StreamSetupPanel from "$lib/components/stream-details/StreamSetupPanel.svelte";
  import type { SourceLocationDraft } from "$lib/source-location";

  const clusters = [
    {
      clusterId: "fixture-eu",
      clusterName: "My EU production cluster",
      accessLevel: "owner",
      allowPrivatePullSources: true,
    },
    {
      clusterId: "fixture-us",
      clusterName: "Marketplace US cluster",
      accessLevel: "subscriber",
      allowPrivatePullSources: false,
    },
  ];
  let pullLocation = $state<SourceLocationDraft>({
    mode: "RESTRICTED",
    clusters: [{ clusterId: "fixture-eu", nodeIds: [] }],
    avoidNodeIds: [],
  });
</script>

<main class="h-screen overflow-hidden">
  <div data-placement-scroll class="h-full overflow-y-auto">
    <div class="max-w-6xl mx-auto p-3 sm:p-6">
      <p class="mb-4 border border-warning p-3 text-sm">
        LOCAL UI FIXTURE · Simulated policy and status. No live API, subscriptions, media work or
        database writes.
      </p>
      <MediaPlacementEditor scope={{ kind: "TENANT" }} />
      <details class="mt-6">
        <summary class="cursor-pointer text-sm text-muted-foreground">
          Capacity-provider consent fixture
        </summary>
        <div class="mt-3"><MediaCapacityConsentEditor clusterId="fixture-eu" /></div>
      </details>
      <details class="mt-6">
        <summary class="cursor-pointer text-sm text-muted-foreground">
          Pull and managed source workflow
        </summary>
        <div class="mt-3 space-y-4">
          <StreamSetupPanel
            clusterOptions={clusters}
            stream={{
              ingestMode: "PULL",
              pullSource: {
                sourceUriRedacted: "rtsp://camera.example/***",
                enabled: true,
                class: "private",
              },
              sourceLocation: pullLocation,
            }}
          />
          <div class="slab p-4">
            <SourceLocationControl
              value={pullLocation}
              {clusters}
              sourceClass="private"
              onchange={(next) => (pullLocation = next)}
            />
          </div>
          <StreamSetupPanel
            clusterOptions={clusters}
            stream={{
              ingestMode: "MANAGED",
              managedSource: {
                sourceKind: "playlist",
                alwaysOn: true,
                placementCount: 1,
              },
              sourceLocation: {
                mode: "RESTRICTED",
                clusters: [{ clusterId: "fixture-eu", nodeIds: [] }],
                avoidNodeIds: [],
              },
            }}
          />
        </div>
      </details>
    </div>
  </div>
</main>
