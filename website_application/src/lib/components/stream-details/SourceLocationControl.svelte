<script lang="ts">
  import { untrack } from "svelte";
  import { GetMediaPlacementOptionsStore, type GetMediaPlacementOptions$result } from "$houdini";
  import type { PullSourcePlacementClass } from "$lib/utils/pull-source";
  import {
    isOwnedCluster,
    offeredClusters,
    sourceLocationProblem,
    type SourceLocationClusterChoice,
    type SourceLocationDraft,
    type SourceLocationNodeOption,
  } from "$lib/source-location";

  let {
    value,
    clusters = [],
    sourceClass = "public",
    disabled = false,
    onchange,
  }: {
    value: SourceLocationDraft;
    clusters?: readonly SourceLocationClusterChoice[];
    sourceClass?: PullSourcePlacementClass;
    disabled?: boolean;
    onchange: (value: SourceLocationDraft) => void;
  } = $props();

  interface NodeList {
    loading: boolean;
    error: string;
    options: SourceLocationNodeOption[];
  }

  // Bounds the node catalogue walk for one cluster; the API pages at most 100.
  const NODE_PAGE_LIMIT = 10;

  const uid = $props.id();
  let nodeLists = $state<Record<string, NodeList>>({});
  let specificNodes = $state<Record<string, boolean>>({});
  const offer = $derived(offeredClusters(clusters, sourceClass));
  const problem = $derived(sourceLocationProblem(value, sourceClass, clusters));
  const selectedIds = $derived(value.clusters.map((cluster) => cluster.clusterId));
  // Selected clusters that are not offered stay visible so they can be removed.
  const unofferedSelections = $derived(
    selectedIds.filter((id) => !offer.offered.some((cluster) => cluster.clusterId === id))
  );

  $effect(() => {
    const owned = value.clusters
      .map((selected) => clusters.find((cluster) => cluster.clusterId === selected.clusterId))
      .filter(
        (cluster): cluster is SourceLocationClusterChoice => !!cluster && isOwnedCluster(cluster)
      );
    untrack(() => {
      for (const cluster of owned) {
        if (!nodeLists[cluster.clusterId]) void loadNodes(cluster.clusterId);
      }
    });
  });

  async function loadNodes(clusterId: string) {
    nodeLists[clusterId] = { loading: true, error: "", options: [] };
    const options: SourceLocationNodeOption[] = [];
    try {
      let after: string | null = null;
      for (let page = 0; page < NODE_PAGE_LIMIT; page++) {
        const result: { data: GetMediaPlacementOptions$result | null } =
          await new GetMediaPlacementOptionsStore().fetch({
            policy: "NetworkOnly",
            variables: {
              scope: { kind: "TENANT" },
              filter: { kind: "NODE", clusterId },
              first: 100,
              after,
            },
          });
        const data = result.data?.mediaPlacementOptions;
        if (data?.__typename !== "MediaPlacementOptionsConnection") {
          const message = data && "message" in data ? data.message : "";
          nodeLists[clusterId] = {
            loading: false,
            error: message || "Nodes for this cluster could not be loaded.",
            options: [],
          };
          return;
        }
        for (const option of data.nodes) {
          if (option.kind !== "NODE") continue;
          if (option.clusterId && option.clusterId !== clusterId) continue;
          if (!options.some((existing) => existing.id === option.id)) {
            options.push({ id: option.id, name: option.name });
          }
        }
        if (!data.pageInfo.hasNextPage || !data.pageInfo.endCursor) break;
        after = data.pageInfo.endCursor;
      }
      nodeLists[clusterId] = { loading: false, error: "", options };
    } catch {
      nodeLists[clusterId] = {
        loading: false,
        error: "Nodes for this cluster could not be loaded.",
        options: [],
      };
    }
  }

  function emit(next: SourceLocationDraft) {
    onchange({
      mode: next.mode,
      clusters: next.clusters.map((cluster) => ({ ...cluster, nodeIds: [...cluster.nodeIds] })),
      avoidNodeIds: [...next.avoidNodeIds],
    });
  }

  function setMode(mode: "ANY" | "RESTRICTED") {
    if (mode === value.mode) return;
    emit(
      mode === "ANY"
        ? { mode: "ANY", clusters: [], avoidNodeIds: [] }
        : { mode: "RESTRICTED", clusters: [], avoidNodeIds: [] }
    );
  }

  function nodeChoices(clusterId: string): SourceLocationNodeOption[] {
    const known = [...(nodeLists[clusterId]?.options ?? [])];
    const selected = value.clusters.find((cluster) => cluster.clusterId === clusterId);
    for (const id of selected?.nodeIds ?? []) {
      if (!known.some((option) => option.id === id)) known.push({ id, name: id });
    }
    return known;
  }

  function avoidChoices(clusterId: string): SourceLocationNodeOption[] {
    return nodeLists[clusterId]?.options ?? [];
  }

  function toggleCluster(clusterId: string, checked: boolean) {
    if (checked) {
      if (selectedIds.includes(clusterId)) return;
      emit({ ...value, clusters: [...value.clusters, { clusterId, nodeIds: [] }] });
      return;
    }
    const clusterNodes = new Set((nodeLists[clusterId]?.options ?? []).map((option) => option.id));
    specificNodes[clusterId] = false;
    emit({
      ...value,
      clusters: value.clusters.filter((cluster) => cluster.clusterId !== clusterId),
      avoidNodeIds: value.avoidNodeIds.filter((id) => !clusterNodes.has(id)),
    });
  }

  function setSpecific(clusterId: string, specific: boolean) {
    specificNodes[clusterId] = specific;
    if (specific) return;
    emit({
      ...value,
      clusters: value.clusters.map((cluster) =>
        cluster.clusterId === clusterId ? { ...cluster, nodeIds: [] } : cluster
      ),
    });
  }

  function isSpecific(clusterId: string): boolean {
    const selected = value.clusters.find((cluster) => cluster.clusterId === clusterId);
    return specificNodes[clusterId] ?? (selected?.nodeIds.length ?? 0) > 0;
  }

  function toggleNode(clusterId: string, nodeId: string, checked: boolean) {
    emit({
      ...value,
      clusters: value.clusters.map((cluster) => {
        if (cluster.clusterId !== clusterId) return cluster;
        const rest = cluster.nodeIds.filter((id) => id !== nodeId);
        return { ...cluster, nodeIds: checked ? [...rest, nodeId] : rest };
      }),
    });
  }

  function toggleAvoid(nodeId: string, checked: boolean) {
    const rest = value.avoidNodeIds.filter((id) => id !== nodeId);
    emit({ ...value, avoidNodeIds: checked ? [...rest, nodeId] : rest });
  }

  function clusterLabel(clusterId: string): string {
    return clusters.find((cluster) => cluster.clusterId === clusterId)?.clusterName || clusterId;
  }
</script>

<fieldset class="space-y-3" {disabled} aria-describedby="{uid}-help">
  <legend class="text-sm font-medium text-foreground">Where can this source be reached?</legend>
  <p id="{uid}-help" class="text-xs text-muted-foreground">
    FrameWorks opens the source from a media node. A camera on a local network, or a file stored on
    one machine, can only be read from the clusters or nodes that can reach it.
  </p>

  <label class="flex items-start gap-3 border border-border/50 p-3">
    <input
      type="radio"
      name="{uid}-mode"
      class="mt-1"
      checked={value.mode === "ANY"}
      disabled={sourceClass === "private"}
      onchange={() => setMode("ANY")}
    />
    <span>
      <span class="block text-sm font-medium">Any cluster FrameWorks chooses</span>
      <span class="block text-xs text-muted-foreground">
        {sourceClass === "private"
          ? "Not available: this source address is private or multicast, so it is not reachable from every cluster."
          : "For sources reachable over the public internet."}
      </span>
    </span>
  </label>

  <label class="flex items-start gap-3 border border-border/50 p-3">
    <input
      type="radio"
      name="{uid}-mode"
      class="mt-1"
      checked={value.mode === "RESTRICTED"}
      onchange={() => setMode("RESTRICTED")}
    />
    <span>
      <span class="block text-sm font-medium">Only these clusters</span>
      <span class="block text-xs text-muted-foreground">
        For LAN cameras, private networks, and node-local files.
      </span>
    </span>
  </label>

  {#if value.mode === "RESTRICTED"}
    <div class="space-y-2 border-l border-[hsl(var(--tn-fg-gutter)/0.3)] pl-3">
      {#if offer.hidden > 0}
        <p class="text-xs text-muted-foreground">
          {offer.hidden}
          {offer.hidden === 1 ? "cluster is" : "clusters are"} hidden because private and multicast sources
          can only run on clusters whose owner allows private pull sources.
        </p>
      {/if}
      {#if offer.offered.length === 0 && unofferedSelections.length === 0}
        <p class="border border-border/50 p-3 text-sm text-muted-foreground">
          {sourceClass === "private"
            ? "None of your connected clusters allow private pull sources. A cluster owner enables this in the cluster's settings."
            : "You have no connected clusters to choose from."}
        </p>
      {/if}

      {#each offer.offered as cluster (cluster.clusterId)}
        {@const selected = selectedIds.includes(cluster.clusterId)}
        {@const owned = isOwnedCluster(cluster)}
        <div class="border border-border/50">
          <label class="flex items-start gap-3 p-3">
            <input
              type="checkbox"
              class="mt-1"
              checked={selected}
              onchange={(event) => toggleCluster(cluster.clusterId, event.currentTarget.checked)}
            />
            <span class="min-w-0">
              <span class="block text-sm font-medium truncate">{cluster.clusterName}</span>
              <span class="block text-xs font-mono text-muted-foreground truncate">
                {cluster.clusterId}{owned ? " · your cluster" : ""}
              </span>
            </span>
          </label>

          {#if selected && owned}
            {@const list = nodeLists[cluster.clusterId]}
            {@const specific = isSpecific(cluster.clusterId)}
            <div
              class="space-y-2 border-t border-[hsl(var(--tn-fg-gutter)/0.3)] px-3 py-2"
              role="group"
              aria-label="Nodes of {cluster.clusterName}"
            >
              <div class="flex flex-wrap gap-4 text-sm">
                <label class="flex items-center gap-2">
                  <input
                    type="radio"
                    name="{uid}-nodes-{cluster.clusterId}"
                    checked={!specific}
                    onchange={() => setSpecific(cluster.clusterId, false)}
                  />
                  Any node
                </label>
                <label class="flex items-center gap-2">
                  <input
                    type="radio"
                    name="{uid}-nodes-{cluster.clusterId}"
                    checked={specific}
                    onchange={() => setSpecific(cluster.clusterId, true)}
                  />
                  Only specific nodes
                </label>
              </div>

              {#if list?.loading}
                <p role="status" class="text-xs text-muted-foreground">Loading nodes…</p>
              {:else if list?.error}
                <p role="alert" class="text-xs text-destructive">{list.error}</p>
              {/if}

              {#if specific}
                {@const choices = nodeChoices(cluster.clusterId)}
                {#if choices.length}
                  <div class="grid gap-1 sm:grid-cols-2">
                    {#each choices as node (node.id)}
                      <label class="flex items-center gap-2 text-sm min-w-0">
                        <input
                          type="checkbox"
                          checked={value.clusters
                            .find((item) => item.clusterId === cluster.clusterId)
                            ?.nodeIds.includes(node.id) ?? false}
                          onchange={(event) =>
                            toggleNode(cluster.clusterId, node.id, event.currentTarget.checked)}
                        />
                        <span class="truncate" title={node.id}>{node.name}</span>
                      </label>
                    {/each}
                  </div>
                {:else if list && !list.loading && !list.error}
                  <p class="text-xs text-muted-foreground">This cluster has no nodes to choose.</p>
                {/if}
                {#if (value.clusters.find((item) => item.clusterId === cluster.clusterId)?.nodeIds.length ?? 0) === 0}
                  <p class="text-xs text-muted-foreground">
                    No node selected yet. Until you select one, the source can run on any node of
                    this cluster.
                  </p>
                {/if}
              {/if}

              {#if avoidChoices(cluster.clusterId).length}
                <details>
                  <summary class="cursor-pointer text-xs text-muted-foreground">Avoid nodes</summary
                  >
                  <div class="mt-2 grid gap-1 sm:grid-cols-2">
                    {#each avoidChoices(cluster.clusterId) as node (node.id)}
                      <label class="flex items-center gap-2 text-sm min-w-0">
                        <input
                          type="checkbox"
                          checked={value.avoidNodeIds.includes(node.id)}
                          onchange={(event) => toggleAvoid(node.id, event.currentTarget.checked)}
                        />
                        <span class="truncate" title={node.id}>{node.name}</span>
                      </label>
                    {/each}
                  </div>
                </details>
              {/if}
            </div>
          {/if}
        </div>
      {/each}

      {#each unofferedSelections as clusterId (clusterId)}
        <label class="flex items-start gap-3 border border-border/50 p-3">
          <input
            type="checkbox"
            class="mt-1"
            checked
            onchange={(event) => toggleCluster(clusterId, event.currentTarget.checked)}
          />
          <span class="min-w-0">
            <span class="block text-sm font-medium truncate">{clusterLabel(clusterId)}</span>
            <span class="block text-xs text-muted-foreground">
              {sourceClass === "private" &&
              clusters.some((cluster) => cluster.clusterId === clusterId)
                ? "Does not allow private pull sources. Remove it to save."
                : "Not in your connected clusters."}
            </span>
          </span>
        </label>
      {/each}

      {#if value.avoidNodeIds.some((id) => !value.clusters.some( (cluster) => avoidChoices(cluster.clusterId).some((node) => node.id === id) ))}
        <p class="text-xs text-muted-foreground">
          This location also avoids nodes that are not listed here.
        </p>
      {/if}
    </div>
  {/if}

  {#if problem}
    <p role="alert" class="text-xs text-destructive">{problem}</p>
  {/if}
</fieldset>
