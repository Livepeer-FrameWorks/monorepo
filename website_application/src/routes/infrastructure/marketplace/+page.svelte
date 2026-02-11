<script lang="ts">
  import { onMount } from "svelte";
  import {
    GetMarketplaceClustersStore,
    SubscribeToClusterStore,
    RequestClusterSubscriptionStore,
  } from "$houdini";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import { Tooltip, TooltipContent, TooltipTrigger } from "$lib/components/ui/tooltip";
  import { toast } from "$lib/stores/toast";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  const marketplaceStore = new GetMarketplaceClustersStore();
  const subscribeMutation = new SubscribeToClusterStore();
  const requestMutation = new RequestClusterSubscriptionStore();

  let clusters = $derived($marketplaceStore.data?.marketplaceClusters ?? []);
  let mutating = $derived($subscribeMutation.fetching || $requestMutation.fetching);

  let publicCount = $derived(clusters.length);
  let freeCount = $derived(clusters.filter((c) => c.pricingModel === "FREE_UNMETERED").length);
  let connectedCount = $derived(clusters.filter((c) => c.isSubscribed).length);
  let eligibleCount = $derived(clusters.filter((c) => c.isEligible && !c.isSubscribed).length);

  onMount(async () => {
    await marketplaceStore.fetch();
  });

  type ClusterType = (typeof clusters)[number];

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

  async function connectToCluster(cluster: ClusterType) {
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
      await marketplaceStore.fetch();
    } catch {
      toast.error("Failed to connect to cluster");
    }
  }

  const StoreIcon = getIconComponent("Store");
  const GlobeIcon = getIconComponent("Globe2");
  const ZapIcon = getIconComponent("Zap");
  const LinkIcon = getIconComponent("Link");
  const SparklesIcon = getIconComponent("Sparkles");
</script>

<svelte:head>
  <title>Cluster Marketplace - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center gap-3">
      <StoreIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Cluster Marketplace</h1>
        <p class="text-sm text-muted-foreground">
          Browse and connect to global video infrastructure
        </p>
      </div>
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    <div class="page-transition">
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
            {#if clusters.length === 0}
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
                    {#each clusters as cluster (cluster.clusterId)}
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
    </div>
  </div>
</div>
