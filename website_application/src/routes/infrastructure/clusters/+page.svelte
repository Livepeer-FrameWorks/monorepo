<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { SvelteMap } from "svelte/reactivity";
  import {
    fragment,
    GetNodesConnectionStore,
    GetClustersAccessStore,
    GetMySubscriptionsStore,
    GetMyClusterInvitesStore,
    GetPendingSubscriptionsStore,
    UnsubscribeFromClusterStore,
    AcceptClusterInviteStore,
    CreatePrivateClusterStore,
    SetPreferredClusterStore,
    ApproveClusterSubscriptionStore,
    RejectClusterSubscriptionStore,
    GetMarketplaceClustersStore,
    SubscribeToClusterStore,
    RequestClusterSubscriptionStore,
    NodeListFieldsStore,
    BootstrapTokenFieldsStore,
  } from "$houdini";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import { Tooltip, TooltipContent, TooltipTrigger } from "$lib/components/ui/tooltip";
  import { toast } from "$lib/stores/toast";
  import RoutingMap from "$lib/components/charts/RoutingMap.svelte";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  // Icons
  const ServerIcon = getIconComponent("Server");
  const LinkIcon = getIconComponent("Link");
  const StarIcon = getIconComponent("Star");
  const MailIcon = getIconComponent("Mail");
  const ShieldIcon = getIconComponent("Shield");
  const PlusIcon = getIconComponent("Plus");
  const CheckIcon = getIconComponent("Check");
  const XIcon = getIconComponent("X");
  const CopyIcon = getIconComponent("Copy");
  const GlobeIcon = getIconComponent("Globe2");
  const ZapIcon = getIconComponent("Zap");
  const SparklesIcon = getIconComponent("Sparkles");

  // My Network stores
  const nodesStore = new GetNodesConnectionStore();
  const accessStore = new GetClustersAccessStore();
  const subscriptionsStore = new GetMySubscriptionsStore();
  const invitesStore = new GetMyClusterInvitesStore();
  const unsubscribeMutation = new UnsubscribeFromClusterStore();
  const acceptInviteMutation = new AcceptClusterInviteStore();
  const createClusterMutation = new CreatePrivateClusterStore();
  const setPreferredMutation = new SetPreferredClusterStore();
  const approveMutation = new ApproveClusterSubscriptionStore();
  const rejectMutation = new RejectClusterSubscriptionStore();

  // Marketplace stores
  const marketplaceStore = new GetMarketplaceClustersStore();
  const subscribeMutation = new SubscribeToClusterStore();
  const requestMutation = new RequestClusterSubscriptionStore();

  // Fragment stores
  const nodeCoreStore = new NodeListFieldsStore();
  const bootstrapTokenStore = new BootstrapTokenFieldsStore();

  function unmaskBootstrapToken(
    masked: { readonly " $fragments": { BootstrapTokenFields: object } } | null | undefined
  ) {
    if (!masked) return null;
    return get(fragment(masked, bootstrapTokenStore));
  }

  // My Network derived state
  let maskedNodes = $derived($nodesStore.data?.nodesConnection?.edges?.map((e) => e.node) ?? []);
  let nodes = $derived(maskedNodes.map((node) => get(fragment(node, nodeCoreStore))));
  let mySubscriptions = $derived($subscriptionsStore.data?.mySubscriptions ?? []);
  let accessList = $derived($accessStore.data?.clustersAccess ?? []);
  let pendingInvites = $derived(
    ($invitesStore.data?.myClusterInvites ?? []).filter((i) => i.status === "pending")
  );

  let accessByCluster = $derived.by(() => {
    const map = new SvelteMap<string, (typeof accessList)[number]>();
    for (const entry of accessList) {
      map.set(entry.clusterId, entry);
    }
    return map;
  });

  let preferredCluster = $derived(mySubscriptions.find((c) => c.isDefaultCluster) ?? null);

  let ownedClusterIds = $derived(
    accessList.filter((a) => a.accessLevel === "owner").map((a) => a.clusterId)
  );

  let pendingApprovalData = $state<
    {
      clusterId: string;
      subscriptions: NonNullable<
        NonNullable<
          ReturnType<typeof get<InstanceType<typeof GetPendingSubscriptionsStore>>>["data"]
        >["pendingSubscriptions"]
      >;
    }[]
  >([]);
  let pendingApprovals = $derived(
    pendingApprovalData.flatMap((entry) =>
      entry.subscriptions.filter((s) => s.subscriptionStatus === "PENDING_APPROVAL")
    )
  );

  // Marketplace derived state
  let marketplaceClusters = $derived($marketplaceStore.data?.marketplaceClusters ?? []);
  let publicCount = $derived(marketplaceClusters.length);
  let freeCount = $derived(
    marketplaceClusters.filter((c) => c.pricingModel === "FREE_UNMETERED").length
  );
  let connectedCount = $derived(marketplaceClusters.filter((c) => c.isSubscribed).length);
  let eligibleCount = $derived(
    marketplaceClusters.filter((c) => c.isEligible && !c.isSubscribed).length
  );

  let mutating = $derived(
    $unsubscribeMutation.fetching ||
      $acceptInviteMutation.fetching ||
      $createClusterMutation.fetching ||
      $setPreferredMutation.fetching ||
      $approveMutation.fetching ||
      $rejectMutation.fetching ||
      $subscribeMutation.fetching ||
      $requestMutation.fetching
  );

  let showCreateClusterModal = $state(false);
  let newClusterName = $state("");
  let createdBootstrapToken = $state<string | null>(null);

  let mapNodes = $derived(
    nodes
      .filter((n) => n.latitude && n.longitude)
      .map((n) => ({
        id: n.id,
        name: n.nodeName,
        lat: n.latitude!,
        lng: n.longitude!,
      }))
  );

  // Tab state
  let activeTab = $state<"my-clusters" | "marketplace">("my-clusters");

  onMount(async () => {
    await Promise.all([
      nodesStore.fetch(),
      accessStore.fetch(),
      subscriptionsStore.fetch(),
      invitesStore.fetch(),
      marketplaceStore.fetch(),
    ]);
    await fetchPendingApprovals();
  });

  async function fetchPendingApprovals() {
    if (ownedClusterIds.length === 0) return;
    const results = await Promise.all(
      ownedClusterIds.map(async (clusterId) => {
        const store = new GetPendingSubscriptionsStore();
        await store.fetch({ variables: { clusterId } });
        const subs = get(store)?.data?.pendingSubscriptions ?? [];
        return { clusterId, subscriptions: [...subs] };
      })
    );
    pendingApprovalData = results;
  }

  type ClusterInviteType = (typeof pendingInvites)[number];
  type MarketplaceClusterType = (typeof marketplaceClusters)[number];

  async function acceptInvite(invite: ClusterInviteType) {
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
          accessStore.fetch(),
          marketplaceStore.fetch(),
        ]);
      } else {
        toast.error("Failed to accept invite");
      }
    } catch {
      toast.error("Failed to accept invite");
    }
  }

  async function disconnectCluster(clusterId: string, clusterName: string) {
    try {
      await unsubscribeMutation.mutate({ clusterId });
      toast.success(`Disconnected from ${clusterName}`);
      await Promise.all([
        subscriptionsStore.fetch(),
        accessStore.fetch(),
        marketplaceStore.fetch(),
      ]);
    } catch {
      toast.error("Failed to disconnect");
    }
  }

  async function setPreferred(clusterId: string, clusterName: string) {
    try {
      const result = await setPreferredMutation.mutate({ clusterId });
      const data = result.data?.setPreferredCluster;
      if (data?.__typename === "Cluster") {
        toast.success(`${clusterName} is now your preferred cluster`);
        await subscriptionsStore.fetch();
      } else if (
        data?.__typename === "ValidationError" ||
        data?.__typename === "NotFoundError" ||
        data?.__typename === "AuthError"
      ) {
        toast.error(data.message);
      }
    } catch {
      toast.error("Failed to set preferred cluster");
    }
  }

  async function approveRequest(subscriptionId: string) {
    try {
      const result = await approveMutation.mutate({ subscriptionId });
      const data = result.data?.approveClusterSubscription;
      if (data?.__typename === "ClusterSubscription") {
        toast.success(`Approved ${data.tenantName} for ${data.clusterName}`);
        await fetchPendingApprovals();
      } else if (
        data?.__typename === "ValidationError" ||
        data?.__typename === "NotFoundError" ||
        data?.__typename === "AuthError"
      ) {
        toast.error(data.message);
      }
    } catch {
      toast.error("Failed to approve request");
    }
  }

  async function rejectRequest(subscriptionId: string) {
    try {
      const result = await rejectMutation.mutate({ subscriptionId });
      const data = result.data?.rejectClusterSubscription;
      if (data?.__typename === "ClusterSubscription") {
        toast.success(`Rejected ${data.tenantName}`);
        await fetchPendingApprovals();
      } else if (
        data?.__typename === "ValidationError" ||
        data?.__typename === "NotFoundError" ||
        data?.__typename === "AuthError"
      ) {
        toast.error(data.message);
      }
    } catch {
      toast.error("Failed to reject request");
    }
  }

  async function connectToCluster(cluster: MarketplaceClusterType) {
    try {
      if (cluster.requiresApproval) {
        const result = await requestMutation.mutate({ clusterId: cluster.clusterId });
        const data = result.data?.requestClusterSubscription;
        if (data?.__typename === "ClusterSubscription") {
          toast.success(`Access requested for ${cluster.clusterName}`);
        } else if (data?.__typename === "ValidationError") {
          toast.error(data.message);
        } else if (data?.__typename === "AuthError") {
          toast.error(data.message);
        } else {
          toast.error("Failed to request access");
        }
      } else {
        await subscribeMutation.mutate({ clusterId: cluster.clusterId });
        toast.success(`Connected to ${cluster.clusterName}`);
      }
      await Promise.all([
        marketplaceStore.fetch(),
        subscriptionsStore.fetch(),
        accessStore.fetch(),
      ]);
    } catch {
      toast.error("Failed to connect to cluster");
    }
  }

  async function createPrivateCluster() {
    if (!newClusterName.trim()) {
      toast.error("Cluster name is required");
      return;
    }
    try {
      const result = await createClusterMutation.mutate({
        input: { clusterName: newClusterName.trim() },
      });
      const data = result.data?.createPrivateCluster;
      if (data?.__typename === "CreatePrivateClusterResponse") {
        const unmaskedToken = unmaskBootstrapToken(data.bootstrapToken);
        createdBootstrapToken = unmaskedToken?.token ?? null;
        toast.success(`Created cluster "${newClusterName}"`);
        await Promise.all([accessStore.fetch(), subscriptionsStore.fetch()]);
        await fetchPendingApprovals();
      } else if (data?.__typename === "ValidationError") {
        toast.error(data.message);
      } else if (data?.__typename === "AuthError") {
        toast.error(data.message);
      }
    } catch {
      toast.error("Failed to create cluster");
    }
  }

  function closeModal() {
    showCreateClusterModal = false;
    newClusterName = "";
    createdBootstrapToken = null;
  }

  function formatTimeAgo(dateStr: string | null | undefined) {
    if (!dateStr) return "Unknown";
    const date = new Date(dateStr);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffSec = Math.floor(diffMs / 1000);
    if (diffSec < 60) return `${diffSec}s ago`;
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
    return `${Math.floor(diffSec / 86400)}d ago`;
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

  function pricingBadgeClass(pricingModel: string): string {
    if (pricingModel === "FREE_UNMETERED") return "bg-success/10 text-success";
    if (pricingModel === "METERED") return "bg-warning/10 text-warning";
    return "bg-primary/10 text-primary";
  }
</script>

<svelte:head>
  <title>Clusters - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center justify-between gap-4">
      <div class="flex items-center gap-3">
        <ServerIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Clusters</h1>
          <p class="text-sm text-muted-foreground">
            Manage connections and browse available infrastructure
          </p>
        </div>
      </div>
      <Button onclick={() => (showCreateClusterModal = true)}>
        <PlusIcon class="w-4 h-4 mr-2" />
        Create Private Cluster
      </Button>
    </div>

    <!-- Tab Switcher -->
    <div class="flex gap-1 mt-3">
      <button
        class="px-4 py-1.5 text-sm font-medium rounded-md transition-colors {activeTab ===
        'my-clusters'
          ? 'bg-primary text-primary-foreground'
          : 'text-muted-foreground hover:text-foreground hover:bg-muted/50'}"
        onclick={() => (activeTab = "my-clusters")}
      >
        My Clusters
        {#if pendingInvites.length > 0}
          <span
            class="ml-1.5 inline-flex items-center justify-center w-5 h-5 text-[0.6rem] font-bold rounded-full bg-info text-white"
          >
            {pendingInvites.length}
          </span>
        {/if}
      </button>
      <button
        class="px-4 py-1.5 text-sm font-medium rounded-md transition-colors {activeTab ===
        'marketplace'
          ? 'bg-primary text-primary-foreground'
          : 'text-muted-foreground hover:text-foreground hover:bg-muted/50'}"
        onclick={() => (activeTab = "marketplace")}
      >
        Marketplace
        {#if eligibleCount > 0}
          <span
            class="ml-1.5 inline-flex items-center justify-center w-5 h-5 text-[0.6rem] font-bold rounded-full bg-success text-white"
          >
            {eligibleCount}
          </span>
        {/if}
      </button>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
    <div class="page-transition">
      {#if activeTab === "my-clusters"}
        <!-- My Clusters Tab -->
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
          <div>
            <DashboardMetricCard
              icon={LinkIcon}
              iconColor="text-primary"
              value={mySubscriptions.length}
              valueColor="text-primary"
              label="Connections"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={StarIcon}
              iconColor="text-warning"
              value={preferredCluster?.clusterName ?? "None"}
              valueColor="text-warning"
              label="Preferred Cluster"
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
          <div>
            <DashboardMetricCard
              icon={ShieldIcon}
              iconColor="text-accent-purple"
              value={pendingApprovals.length}
              valueColor="text-accent-purple"
              label="Pending Approvals"
            />
          </div>
        </GridSeam>

        <div class="dashboard-grid">
          <!-- Preferred Cluster Banner -->
          {#if preferredCluster}
            <div class="slab col-span-full">
              <div class="slab-body--padded">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-3">
                    <StarIcon class="w-5 h-5 text-warning" />
                    <div>
                      <p class="font-medium text-foreground">
                        {preferredCluster.clusterName}
                        <span class="text-muted-foreground font-normal ml-1">
                          is your preferred cluster
                        </span>
                      </p>
                      <p class="text-sm text-muted-foreground">
                        DNS steers viewers here. Ingest and playback URIs for this cluster appear
                        first.
                      </p>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          {/if}

          <!-- Pending Invitations -->
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
                        <th class="py-3 px-4 font-medium text-muted-foreground text-right">
                          Action
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {#each pendingInvites as invite (invite.id)}
                        <tr class="border-b border-border/30 hover:bg-muted/20">
                          <td class="py-3 px-4 font-medium text-foreground">
                            {invite.clusterName}
                          </td>
                          <td class="py-3 px-4">
                            <span
                              class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-info/10 text-info"
                            >
                              {invite.accessLevel}
                            </span>
                          </td>
                          <td class="py-3 px-4 text-muted-foreground text-sm">
                            {invite.expiresAt
                              ? new Date(invite.expiresAt).toLocaleDateString()
                              : "Never"}
                          </td>
                          <td class="py-3 px-4 text-right">
                            <Button
                              variant="default"
                              size="sm"
                              disabled={mutating}
                              onclick={() => acceptInvite(invite)}
                            >
                              <CheckIcon class="w-3 h-3 mr-1" />
                              Accept
                            </Button>
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              </div>
            </div>
          {/if}

          <!-- My Subscriptions -->
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
                    description="You haven't connected to any clusters yet. Switch to Marketplace to find video infrastructure."
                  />
                </div>
              {:else}
                <div class="overflow-x-auto">
                  <table class="w-full text-sm text-left">
                    <thead class="bg-muted/30 border-b border-border">
                      <tr>
                        <th class="py-3 px-4 font-medium text-muted-foreground">Cluster</th>
                        <th class="py-3 px-4 font-medium text-muted-foreground">Status</th>
                        <th class="py-3 px-4 font-medium text-muted-foreground">Access</th>
                        <th class="py-3 px-4 font-medium text-muted-foreground">Preferred</th>
                        <th class="py-3 px-4 font-medium text-muted-foreground text-right">
                          Action
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {#each mySubscriptions as cluster (cluster.clusterId)}
                        {@const isOwner =
                          accessByCluster.get(cluster.clusterId)?.accessLevel === "owner"}
                        {@const isPreferred = cluster.isDefaultCluster}
                        <tr class="border-b border-border/30 hover:bg-muted/20">
                          <td class="py-3 px-4 font-medium text-foreground">
                            <div class="flex items-center gap-2">
                              {#if isPreferred}
                                <StarIcon class="w-4 h-4 text-warning" />
                              {:else}
                                <div class="w-2 h-2 rounded-full bg-success"></div>
                              {/if}
                              {cluster.clusterName}
                            </div>
                          </td>
                          <td class="py-3 px-4">
                            <span
                              class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success/10 text-success"
                            >
                              {cluster.healthStatus ?? "Active"}
                            </span>
                          </td>
                          <td class="py-3 px-4 text-muted-foreground">
                            {accessByCluster.get(cluster.clusterId)?.accessLevel ?? "subscriber"}
                          </td>
                          <td class="py-3 px-4">
                            {#if isPreferred}
                              <span
                                class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-warning/10 text-warning"
                              >
                                Preferred
                              </span>
                            {:else}
                              <Button
                                variant="ghost"
                                size="sm"
                                disabled={mutating}
                                onclick={() => setPreferred(cluster.clusterId, cluster.clusterName)}
                              >
                                Set
                              </Button>
                            {/if}
                          </td>
                          <td class="py-3 px-4 text-right">
                            {#if isOwner}
                              <Button variant="secondary" size="sm" disabled class="opacity-70">
                                Owned
                              </Button>
                            {:else}
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={mutating}
                                onclick={() =>
                                  disconnectCluster(cluster.clusterId, cluster.clusterName)}
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

          <!-- Owner Approvals -->
          {#if ownedClusterIds.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <h3 class="flex items-center gap-2">
                  <ShieldIcon class="w-4 h-4 text-accent-purple" />
                  Pending Approvals
                </h3>
              </div>
              <div class="slab-body--flush">
                {#if pendingApprovals.length === 0}
                  <div class="p-8">
                    <EmptyState
                      iconName="Shield"
                      title="No pending requests"
                      description="Subscription requests for your clusters will appear here."
                    />
                  </div>
                {:else}
                  <div class="overflow-x-auto">
                    <table class="w-full text-sm text-left">
                      <thead class="bg-muted/30 border-b border-border">
                        <tr>
                          <th class="py-3 px-4 font-medium text-muted-foreground">Tenant</th>
                          <th class="py-3 px-4 font-medium text-muted-foreground">Cluster</th>
                          <th class="py-3 px-4 font-medium text-muted-foreground">Requested</th>
                          <th class="py-3 px-4 font-medium text-muted-foreground text-right">
                            Action
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each pendingApprovals as request (request.id)}
                          <tr class="border-b border-border/30 hover:bg-muted/20">
                            <td class="py-3 px-4 font-medium text-foreground">
                              {request.tenantName ?? request.tenantId}
                            </td>
                            <td class="py-3 px-4 text-muted-foreground">
                              {request.clusterName ?? request.clusterId}
                            </td>
                            <td class="py-3 px-4 text-muted-foreground">
                              {formatTimeAgo(request.requestedAt)}
                            </td>
                            <td class="py-3 px-4 text-right">
                              <div class="flex items-center justify-end gap-2">
                                <Button
                                  variant="default"
                                  size="sm"
                                  disabled={mutating}
                                  onclick={() => approveRequest(request.id)}
                                >
                                  <CheckIcon class="w-3 h-3 mr-1" />
                                  Approve
                                </Button>
                                <Button
                                  variant="outline"
                                  size="sm"
                                  disabled={mutating}
                                  onclick={() => rejectRequest(request.id)}
                                >
                                  <XIcon class="w-3 h-3 mr-1" />
                                  Reject
                                </Button>
                              </div>
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                {/if}
              </div>
            </div>
          {/if}

          <!-- Global Coverage Map -->
          <div class="slab col-span-full">
            <div class="slab-header">
              <h3>Global Coverage</h3>
            </div>
            <div class="slab-body--flush h-[350px]">
              {#if typeof window !== "undefined"}
                <RoutingMap nodes={mapNodes} routes={[]} height={350} />
              {/if}
            </div>
          </div>
        </div>
      {:else}
        <!-- Marketplace Tab -->
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
          <div>
            <DashboardMetricCard
              icon={GlobeIcon}
              iconColor="text-primary"
              value={publicCount}
              valueColor="text-primary"
              label="Available Clusters"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={ZapIcon}
              iconColor="text-success"
              value={freeCount}
              valueColor="text-success"
              label="Free Clusters"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={LinkIcon}
              iconColor="text-warning"
              value={connectedCount}
              valueColor="text-warning"
              label="Connected"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={SparklesIcon}
              iconColor="text-info"
              value={eligibleCount}
              valueColor="text-info"
              label="Eligible to Join"
            />
          </div>
        </GridSeam>

        <div class="dashboard-grid">
          <div class="slab col-span-full">
            <div class="slab-header">
              <h3>Available Clusters</h3>
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
                        <th class="py-3 px-4 font-medium text-muted-foreground text-right">
                          Action
                        </th>
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
                            <span
                              class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium {pricingBadgeClass(
                                cluster.pricingModel
                              )}"
                            >
                              {formatPrice(cluster.pricingModel, cluster.monthlyPriceCents)}
                            </span>
                          </td>
                          <td class="py-3 px-4 text-right">
                            {#if cluster.isSubscribed}
                              <Button variant="outline" size="sm" disabled class="opacity-70">
                                Connected
                              </Button>
                            {:else if cluster.subscriptionStatus === "PENDING_APPROVAL"}
                              <Button variant="outline" size="sm" disabled class="opacity-70">
                                Pending...
                              </Button>
                            {:else if !cluster.isEligible}
                              <Tooltip>
                                <TooltipTrigger>
                                  <Button variant="secondary" size="sm" disabled class="opacity-50">
                                    Ineligible
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>
                                  {cluster.denialReason ??
                                    "Your billing tier does not include access to this cluster"}
                                </TooltipContent>
                              </Tooltip>
                            {:else if cluster.requiresApproval}
                              <Button
                                variant="default"
                                size="sm"
                                disabled={mutating}
                                onclick={() => connectToCluster(cluster)}
                              >
                                Request Access
                              </Button>
                            {:else}
                              <Button
                                variant="default"
                                size="sm"
                                disabled={mutating}
                                onclick={() => connectToCluster(cluster)}
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
        </div>
      {/if}
    </div>
  </div>
</div>

<!-- Create Private Cluster Modal -->
{#if showCreateClusterModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center">
    <button
      type="button"
      class="absolute inset-0 bg-black/50 cursor-default"
      onclick={closeModal}
      aria-label="Close modal"
    ></button>
    <div
      class="relative bg-background border border-border rounded-lg shadow-xl max-w-md w-full mx-4 p-6"
    >
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
