<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import {
    fragment,
    GetNodesConnectionStore,
    GetClustersAvailableStore,
    GetClustersAccessStore,
    GetMySubscriptionsStore,
    GetMarketplaceClustersStore,
    GetMyClusterInvitesStore,
    SubscribeToClusterStore,
    UnsubscribeFromClusterStore,
    AcceptClusterInviteStore,
    CreatePrivateClusterStore,
    NodeCoreFieldsStore,
    BootstrapTokenFieldsStore
  } from "$houdini";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import { toast } from "$lib/stores/toast";
  import RoutingMap from "$lib/components/charts/RoutingMap.svelte";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  const nodesStore = new GetNodesConnectionStore();
  const availableStore = new GetClustersAvailableStore();
  const accessStore = new GetClustersAccessStore();
  const subscriptionsStore = new GetMySubscriptionsStore();
  const marketplaceStore = new GetMarketplaceClustersStore();
  const invitesStore = new GetMyClusterInvitesStore();
  const subscribeMutation = new SubscribeToClusterStore();
  const unsubscribeMutation = new UnsubscribeFromClusterStore();
  const acceptInviteMutation = new AcceptClusterInviteStore();
  const createClusterMutation = new CreatePrivateClusterStore();

  // Fragment stores for unmasking
  const nodeCoreStore = new NodeCoreFieldsStore();
  const bootstrapTokenStore = new BootstrapTokenFieldsStore();

  // Helper to unmask bootstrap token
  function unmaskBootstrapToken(masked: { readonly " $fragments": { BootstrapTokenFields: {} } } | null | undefined) {
    if (!masked) return null;
    return get(fragment(masked, bootstrapTokenStore));
  }

  let maskedNodes = $derived($nodesStore.data?.nodesConnection?.edges?.map(e => e.node) ?? []);
  // Unmask node core fields
  let nodes = $derived(
    maskedNodes.map(node => get(fragment(node, nodeCoreStore)))
  );
  let availableClusters = $derived($availableStore.data?.clustersAvailable ?? []);
  let mySubscriptions = $derived($subscriptionsStore.data?.mySubscriptions ?? []);
  let accessList = $derived($accessStore.data?.clustersAccess ?? []);
  let marketplaceClusters = $derived($marketplaceStore.data?.marketplaceClusters ?? []);
  let pendingInvites = $derived(($invitesStore.data?.myClusterInvites ?? []).filter(i => i.status === "pending"));

  let subscribedIds = $derived(new Set(mySubscriptions.map(c => c.clusterId)));
  let accessByCluster = $derived.by(() => {
    const map = new Map<string, (typeof accessList)[number]>();
    for (const entry of accessList) {
      map.set(entry.clusterId, entry);
    }
    return map;
  });

  let loading = $derived($nodesStore.fetching || $availableStore.fetching || $accessStore.fetching);
  let mutating = $derived(
    $subscribeMutation.fetching ||
    $unsubscribeMutation.fetching ||
    $acceptInviteMutation.fetching ||
    $createClusterMutation.fetching
  );

  // Modal state
  let showCreateClusterModal = $state(false);
  let newClusterName = $state("");
  let createdBootstrapToken = $state<string | null>(null);

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
      subscriptionsStore.fetch(),
      marketplaceStore.fetch(),
      invitesStore.fetch()
    ]);
  });

  function clusterState(cluster: any) {
    const access = accessByCluster.get(cluster.clusterId);
    if (access?.accessLevel === "owner") return "owner";
    if (subscribedIds.has(cluster.clusterId)) return "subscribed";
    return "available";
  }

  function formatPrice(pricingModel: string, priceCents?: number | null): string {
    if (pricingModel === "FREE_UNMETERED") return "Free";
    if (pricingModel === "TIER_INHERIT") return "Tier-based";
    if (pricingModel === "METERED") return "Usage-based";
    if (pricingModel === "MONTHLY" && priceCents) {
      return `$${(priceCents / 100).toFixed(2)}/mo`;
    }
    return pricingModel;
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
        accessStore.fetch(),
        marketplaceStore.fetch()
      ]);
    } catch (e) {
      toast.error("Failed to update subscription");
    }
  }

  async function acceptInvite(invite: any) {
    if (!invite.inviteToken) {
      toast.error("Invalid invite token");
      return;
    }
    try {
      const result = await acceptInviteMutation.mutate({ inviteToken: invite.inviteToken });
      if (result.data?.acceptClusterInvite?.__typename === "ClusterSubscription") {
        toast.success(`Joined ${invite.clusterName}`);
        await Promise.all([
          invitesStore.fetch(),
          subscriptionsStore.fetch(),
          accessStore.fetch()
        ]);
      } else {
        toast.error("Failed to accept invite");
      }
    } catch (e) {
      toast.error("Failed to accept invite");
    }
  }

  async function createPrivateCluster() {
    if (!newClusterName.trim()) {
      toast.error("Cluster name is required");
      return;
    }
    try {
      const result = await createClusterMutation.mutate({
        input: {
          clusterName: newClusterName.trim()
        }
      });
      const data = result.data?.createPrivateCluster;
      if (data?.__typename === "CreatePrivateClusterResponse") {
        const unmaskedToken = unmaskBootstrapToken(data.bootstrapToken);
        createdBootstrapToken = unmaskedToken?.token ?? null;
        toast.success(`Created cluster "${newClusterName}"`);
        await Promise.all([
          accessStore.fetch(),
          subscriptionsStore.fetch()
        ]);
      } else if (data?.__typename === "ValidationError") {
        toast.error(data.message);
      } else if (data?.__typename === "AuthError") {
        toast.error(data.message);
      }
    } catch (e) {
      toast.error("Failed to create cluster");
    }
  }

  function closeModal() {
    showCreateClusterModal = false;
    newClusterName = "";
    createdBootstrapToken = null;
  }

  const GlobeIcon = getIconComponent("Globe2");
  const ServerIcon = getIconComponent("Server");
  const NetworkIcon = getIconComponent("Network");
  const ActivityIcon = getIconComponent("Activity");
  const MailIcon = getIconComponent("Mail");
  const PlusIcon = getIconComponent("Plus");
  const CheckIcon = getIconComponent("Check");
  const XIcon = getIconComponent("X");
  const CopyIcon = getIconComponent("Copy");
</script>

<svelte:head>
  <title>Network Explorer - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
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
      <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
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
            value={marketplaceClusters.length}
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
        <div>
          <DashboardMetricCard
            icon={MailIcon}
            iconColor="text-info"
            value={pendingInvites.length}
            valueColor="text-info"
            label="Pending Invites"
          />
        </div>
      </GridSeam>

      <div class="dashboard-grid">
        <!-- Pending Invitations Slab (only show if invites exist) -->
        {#if pendingInvites.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header bg-info/10">
              <h3 class="flex items-center gap-2">
                <MailIcon class="w-4 h-4 text-info" />
                Pending Invitations
              </h3>
            </div>
            <div class="slab-body--flush">
              <div class="overflow-x-auto">
                <table class="w-full text-sm text-left">
                  <thead class="bg-muted/30 border-b border-border">
                    <tr>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Cluster</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Access Level</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Expires</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground text-right">Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each pendingInvites as invite (invite.id)}
                      <tr class="border-b border-border/30 hover:bg-muted/20">
                        <td class="py-3 px-4 font-medium text-foreground">{invite.clusterName}</td>
                        <td class="py-3 px-4">
                          <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-info/10 text-info">
                            {invite.accessLevel}
                          </span>
                        </td>
                        <td class="py-3 px-4 text-muted-foreground text-sm">
                          {invite.expiresAt ? new Date(invite.expiresAt).toLocaleDateString() : "Never"}
                        </td>
                        <td class="py-3 px-4 text-right">
                          <div class="flex items-center justify-end gap-2">
                            <Button
                              variant="default"
                              size="sm"
                              disabled={mutating}
                              onclick={() => acceptInvite(invite)}
                            >
                              <CheckIcon class="w-3 h-3 mr-1" />
                              Accept
                            </Button>
                          </div>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        {/if}

        <!-- My Subscriptions Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <h3>My Subscriptions</h3>
          </div>
          <div class="slab-body--flush">
            {#if mySubscriptions.length === 0}
              <div class="p-8">
                <EmptyState
                  iconName="Network"
                  title="No active connections"
                  description="You haven't connected to any clusters yet. Browse the marketplace below to find video infrastructure."
                />
              </div>
            {:else}
              <div class="overflow-x-auto">
                <table class="w-full text-sm text-left">
                  <thead class="bg-muted/30 border-b border-border">
                    <tr>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Cluster Name</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Status</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Access</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground text-right">Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each mySubscriptions as cluster (cluster.clusterId)}
                      <tr class="border-b border-border/30 hover:bg-muted/20">
                        <td class="py-3 px-4 font-medium text-foreground">
                          <div class="flex items-center gap-2">
                            <div class="w-2 h-2 rounded-full bg-success"></div>
                            {cluster.clusterName}
                          </div>
                        </td>
                        <td class="py-3 px-4">
                          <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success/10 text-success">
                            {cluster.healthStatus ?? "Active"}
                          </span>
                        </td>
                        <td class="py-3 px-4 text-muted-foreground">
                          {accessByCluster.get(cluster.clusterId)?.accessLevel ?? "subscriber"}
                        </td>
                        <td class="py-3 px-4 text-right">
                          {#if accessByCluster.get(cluster.clusterId)?.accessLevel === "owner"}
                            <Button variant="secondary" size="sm" disabled class="opacity-70">
                              Owned
                            </Button>
                          {:else}
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={mutating}
                              onclick={() => toggleSubscription(cluster)}
                            >
                              Disconnect
                            </Button>
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          </div>
        </div>

        <!-- Map Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <h3>Global Coverage</h3>
          </div>
          <div class="slab-body--flush h-[350px]">
            {#if typeof window !== 'undefined'}
              <RoutingMap
                nodes={mapNodes}
                routes={[]}
                height={350}
              />
            {/if}
          </div>
        </div>

        <!-- Marketplace Clusters Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <h3>Cluster Marketplace</h3>
          </div>
          <div class="slab-body--flush">
            {#if marketplaceClusters.length === 0}
              <div class="p-8">
                <EmptyState
                  iconName="Globe2"
                  title="Marketplace unavailable"
                  description="No public clusters are currently available. Check back later."
                />
              </div>
            {:else}
              <div class="overflow-x-auto">
                <table class="w-full text-sm text-left">
                  <thead class="bg-muted/30 border-b border-border">
                    <tr>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Cluster</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Description</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Operator</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground">Pricing</th>
                      <th class="py-3 px-4 font-medium text-muted-foreground text-right">Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each marketplaceClusters as cluster (cluster.clusterId)}
                      <tr class="border-b border-border/30 hover:bg-muted/20">
                        <td class="py-3 px-4 font-medium text-foreground">
                          <div class="flex items-center gap-2">
                            <div class="w-2 h-2 rounded-full bg-success"></div>
                            {cluster.clusterName}
                          </div>
                        </td>
                        <td class="py-3 px-4 text-muted-foreground max-w-xs truncate">
                          {cluster.shortDescription ?? "-"}
                        </td>
                        <td class="py-3 px-4 text-muted-foreground">
                          {cluster.ownerName ?? "Platform"}
                        </td>
                        <td class="py-3 px-4">
                          <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-primary/10 text-primary">
                            {formatPrice(cluster.pricingModel, cluster.monthlyPriceCents)}
                          </span>
                        </td>
                        <td class="py-3 px-4 text-right">
                          {#if cluster.isSubscribed}
                            <Button variant="outline" size="sm" disabled class="opacity-70">
                              Connected
                            </Button>
                          {:else}
                            <Button
                              variant="default"
                              size="sm"
                              disabled={mutating}
                              onclick={() => toggleSubscription(cluster)}
                            >
                              Connect
                            </Button>
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          </div>
        </div>

        <!-- Self-Hosted Edge Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <h3>Self-Hosted Edge</h3>
          </div>
          <div class="slab-body--padded">
            <div class="flex items-center justify-between">
              <div>
                <p class="text-muted-foreground">
                  Deploy your own edge infrastructure and connect it to FrameWorks.
                </p>
                <p class="text-sm text-muted-foreground mt-1">
                  Create a private cluster to get a bootstrap token for your edge nodes.
                </p>
              </div>
              <Button onclick={() => showCreateClusterModal = true}>
                <PlusIcon class="w-4 h-4 mr-2" />
                Create Private Cluster
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</div>

<!-- Create Cluster Modal -->
{#if showCreateClusterModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center">
    <button
      type="button"
      class="absolute inset-0 bg-black/50 cursor-default"
      onclick={closeModal}
      aria-label="Close modal"
    ></button>
    <div class="relative bg-background border border-border rounded-lg shadow-xl max-w-md w-full mx-4 p-6">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-lg font-semibold">Create Private Cluster</h2>
        <button onclick={closeModal} class="text-muted-foreground hover:text-foreground">
          <XIcon class="w-5 h-5" />
        </button>
      </div>

      {#if createdBootstrapToken}
        <div class="space-y-4">
          <div class="p-4 bg-success/10 border border-success/20 rounded-lg">
            <p class="text-sm text-success font-medium mb-2">Cluster created successfully!</p>
            <p class="text-xs text-muted-foreground mb-3">
              Copy the bootstrap token below. This is the only time it will be shown.
            </p>
            <div class="flex items-center gap-2">
              <code class="flex-1 p-2 bg-muted rounded text-xs font-mono break-all">
                {createdBootstrapToken}
              </code>
              <Button
                variant="outline"
                size="sm"
                onclick={() => {
                  navigator.clipboard.writeText(createdBootstrapToken!);
                  toast.success("Token copied to clipboard");
                }}
              >
                <CopyIcon class="w-4 h-4" />
              </Button>
            </div>
          </div>
          <Button class="w-full" onclick={closeModal}>Done</Button>
        </div>
      {:else}
        <div class="space-y-4">
          <div>
            <label for="clusterName" class="block text-sm font-medium text-foreground mb-1">
              Cluster Name
            </label>
            <input
              id="clusterName"
              type="text"
              bind:value={newClusterName}
              placeholder="e.g., My Edge Cluster"
              class="w-full px-3 py-2 bg-muted border border-border rounded-md text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary"
            />
          </div>
          <div class="flex gap-3">
            <Button variant="outline" class="flex-1" onclick={closeModal}>Cancel</Button>
            <Button
              class="flex-1"
              disabled={!newClusterName.trim() || mutating}
              onclick={createPrivateCluster}
            >
              {#if mutating}
                Creating...
              {:else}
                Create Cluster
              {/if}
            </Button>
          </div>
        </div>
      {/if}
    </div>
  </div>
{/if}
