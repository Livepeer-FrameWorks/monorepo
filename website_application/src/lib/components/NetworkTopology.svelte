<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { GetNetworkStatusStore } from "$houdini";
  import RoutingMap from "$lib/components/charts/RoutingMap.svelte";
  import type { ExplorerViewport } from "../../../../packages/map-core/network-explorer";
  let { viewport }: { viewport: ExplorerViewport } = $props();
  const network = new GetNetworkStatusStore();
  let error = $state("");
  let topology = $derived($network.data?.networkStatus);
  let clusters = $derived(
    (topology?.clusters ?? []).map((c) => ({
      id: c.clusterId,
      name: c.name,
      region: c.region,
      lat: c.latitude,
      lng: c.longitude,
      nodeCount: c.nodeCount,
      healthyNodeCount: c.healthyNodeCount,
      status: c.status,
      activeStreams: c.currentStreams ?? 0,
      activeViewers: c.currentViewers ?? 0,
      peerCount: c.peerCount,
      clusterType: c.clusterType ?? undefined,
      services: c.services ?? [],
    }))
  );
  let nodes = $derived(
    (topology?.nodes ?? []).map((n) => ({
      id: n.nodeId,
      name: n.name,
      lat: n.latitude,
      lng: n.longitude,
      clusterId: n.clusterId,
      nodeType: n.nodeType,
      status: n.status,
    }))
  );
  let relationships = $derived.by(() => {
    return (topology?.peerConnections ?? []).flatMap((p) => {
      const from = clusters.find((c) => c.id === p.sourceCluster);
      const to = clusters.find((c) => c.id === p.targetCluster);
      if (!from || !to || (from.lat === 0 && from.lng === 0) || (to.lat === 0 && to.lng === 0))
        return [];
      return [
        {
          sourceClusterId: from.id,
          targetClusterId: to.id,
          from: [from.lat, from.lng] as [number, number],
          to: [to.lat, to.lng] as [number, number],
          type: p.connectionType === "assignment" ? ("assignment" as const) : ("peering" as const),
          active: p.connected,
        },
      ];
    });
  });
  onMount(() => {
    let disposed = false;
    const refresh = async () => {
      try {
        const response = await network.fetch();
        if (!disposed)
          error =
            response.errors?.length || !response.data?.networkStatus
              ? "Topology is temporarily unavailable."
              : "";
      } catch {
        if (!disposed) error = "Topology is temporarily unavailable.";
      }
    };
    void refresh();
    const timer = setInterval(() => void refresh(), 30000);
    return () => {
      disposed = true;
      clearInterval(timer);
    };
  });
</script>

<p class="text-muted-foreground mb-4">
  Infrastructure placement and cluster connections. <a
    class="text-primary underline"
    href={resolve("/infrastructure/federation")}>Open federation diagnostics</a
  >
  ·
  <a class="text-primary underline" href={resolve("/infrastructure/network")}
    >Manage infrastructure</a
  >
</p>
{#if error}<p role="status" class="mb-4">{error}</p>{/if}
{#if topology}
  <div class="border border-border">
    <RoutingMap
      routes={[]}
      {nodes}
      {clusters}
      {relationships}
      {viewport}
      serviceInstances={topology.serviceInstances}
      height={500}
      center={[25, 10]}
    />
    <p class="p-3 border-t border-border text-muted-foreground text-sm">
      {topology.healthyNodes}/{topology.totalNodes} nodes healthy · {clusters.length} clusters · {topology.peerConnections.filter(
        (p) => p.connected
      ).length} connected peers
    </p>
  </div>
{:else if !error}<p role="status">Loading topology…</p>{/if}
