<script lang="ts">
  import { onMount } from "svelte";
  import {
    GetNodesStore,
    GetClustersAvailableStore,
    GetClustersAccessStore,
    GetMySubscriptionsStore,
    SubscribeToClusterStore,
    UnsubscribeFromClusterStore
  } from "$houdini";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import { toast } from "$lib/stores/toast";
  import RoutingMap from "$lib/components/charts/RoutingMap.svelte";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";

  const nodesStore = new GetNodesStore();
  const availableStore = new GetClustersAvailableStore();
  const accessStore = new GetClustersAccessStore();
  const subscriptionsStore = new GetMySubscriptionsStore();
  const subscribeMutation = new SubscribeToClusterStore();
  const unsubscribeMutation = new UnsubscribeFromClusterStore();

  let nodes = $derived($nodesStore.data?.nodes ?? []);
  let availableClusters = $derived($availableStore.data?.clustersAvailable ?? []);
  let mySubscriptions = $derived($subscriptionsStore.data?.mySubscriptions ?? []);
  let accessList = $derived($accessStore.data?.clustersAccess ?? []);

  let subscribedIds = $derived(new Set(mySubscriptions.map(c => c.clusterId)));
  let accessByCluster = $derived.by(() => {
    const map = new Map<string, (typeof accessList)[number]>();
    for (const entry of accessList) {
      map.set(entry.clusterId, entry);
    }
    return map;
  });

  let loading = $derived($nodesStore.fetching || $availableStore.fetching || $accessStore.fetching);
  let mutating = $derived($subscribeMutation.fetching || $unsubscribeMutation.fetching);

  // Map Data
  let mapNodes = $derived(nodes
    .filter(n => n.latitude && n.longitude)
    .map(n => ({
      id: n.id,
      name: n.nodeName,
      lat: n.latitude!,
      lng: n.longitude!
    }))
  );

  onMount(async () => {
    await Promise.all([
      nodesStore.fetch(),
      availableStore.fetch(),
      accessStore.fetch(),
      subscriptionsStore.fetch()
    ]);
  });

  function clusterState(cluster: any) {
    const access = accessByCluster.get(cluster.clusterId);
    if (access?.accessLevel === "owner") return "owner";
    if (subscribedIds.has(cluster.clusterId)) return "subscribed";
    return "available";
  }

  async function toggleSubscription(cluster: any) {
    const state = clusterState(cluster);
    if (state === "owner") return;
    const isSubscribed = state === "subscribed";
    try {
        if (isSubscribed) {
            await unsubscribeMutation.mutate({ clusterId: cluster.clusterId });
            toast.success(`Disconnected from ${cluster.clusterName}`);
        } else {
            await subscribeMutation.mutate({ clusterId: cluster.clusterId });
            toast.success(`Connected to ${cluster.clusterName}`);
        }
        await Promise.all([
          subscriptionsStore.fetch(),
          accessStore.fetch()
        ]);
    } catch (e) {
        toast.error("Failed to update subscription");
    }
  }

  const GlobeIcon = getIconComponent("Globe2");
  const ServerIcon = getIconComponent("Server");
  const NetworkIcon = getIconComponent("Network");
  const ActivityIcon = getIconComponent("Activity");
</script>

<svelte:head>
  <title>Network Explorer - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-border shrink-0">
    <div class="flex items-center gap-3">
      <NetworkIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Network Explorer</h1>
        <p class="text-sm text-muted-foreground">
          Discover and connect to global video infrastructure
        </p>
      </div>
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    <div class="page-transition">
      
      <!-- Metrics -->
      <GridSeam cols={3} stack="2x2" surface="panel" flush={true} class="mb-0">
        <div>
            <DashboardMetricCard
                icon={ServerIcon}
                iconColor="text-primary"
                value={nodes.length}
                valueColor="text-primary"
                label="Active Edge Nodes"
            />
        </div>
        <div>
            <DashboardMetricCard
                icon={GlobeIcon}
                iconColor="text-success"
                value={availableClusters.length}
                valueColor="text-success"
                label="Public Clusters"
            />
        </div>
        <div>
            <DashboardMetricCard
                icon={ActivityIcon}
                iconColor="text-warning"
                value={mySubscriptions.length}
                valueColor="text-warning"
                label="Your Connections"
            />
        </div>
      </GridSeam>

      <div class="dashboard-grid">
        <!-- Map Slab -->
        <div class="slab col-span-full">
            <div class="slab-header">
                <h3>Global Coverage</h3>
            </div>
            <div class="slab-body--flush h-[400px]">
                {#if typeof window !== 'undefined'}
                    <RoutingMap 
                        nodes={mapNodes} 
                        routes={[]} 
                        height={400} 
                    />
                {/if}
            </div>
        </div>

        <!-- Available Clusters Slab -->
        <div class="slab col-span-full">
            <div class="slab-header">
                <h3>Public Infrastructure</h3>
            </div>
            <div class="slab-body--flush">
                <div class="overflow-x-auto">
                    <table class="w-full text-sm text-left">
                        <thead class="bg-muted/30 border-b border-border">
                            <tr>
                                <th class="py-3 px-4 font-medium text-muted-foreground">Cluster Name</th>
                                <th class="py-3 px-4 font-medium text-muted-foreground">ID</th>
                                <th class="py-3 px-4 font-medium text-muted-foreground">Tiers</th>
                                <th class="py-3 px-4 font-medium text-muted-foreground text-right">Action</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each availableClusters as cluster (cluster.clusterId)}
                                <tr class="border-b border-border/30 hover:bg-muted/20">
                                    <td class="py-3 px-4 font-medium text-foreground">
                                        <div class="flex items-center gap-2">
                                            <div class="w-2 h-2 rounded-full bg-success"></div>
                                            {cluster.clusterName}
                                        </div>
                                    </td>
                                    <td class="py-3 px-4 font-mono text-xs text-muted-foreground">{cluster.clusterId}</td>
                                    <td class="py-3 px-4">
                                        {#each cluster.tiers || [] as tier}
                                            <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-primary/10 text-primary mr-1">
                                                {tier}
                                            </span>
                                        {/each}
                                    </td>
                                    <td class="py-3 px-4 text-right">
                                        {#if clusterState(cluster) === "owner"}
                                            <Button variant="secondary" size="sm" disabled class="opacity-70">
                                                Owned
                                            </Button>
                                        {:else}
                                            <Button 
                                                variant={clusterState(cluster) === "subscribed" ? "outline" : "default"}
                                                size="sm"
                                                disabled={mutating}
                                                onclick={() => toggleSubscription(cluster)}
                                            >
                                                {clusterState(cluster) === "subscribed" ? "Disconnect" : "Connect"}
                                            </Button>
                                        {/if}
                                    </td>
                                </tr>
                            {/each}
                        </tbody>
                    </table>
                </div>
            </div>
        </div>
      </div>
    </div>
  </div>
</div>
