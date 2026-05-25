<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { resolve } from "$app/paths";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { get } from "svelte/store";
  import {
    fragment,
    GetClustersAccessStore,
    GetInfrastructureOverviewStore,
    GetNetworkStatusStore,
    GetNodesConnectionStore,
    GetServiceInstancesConnectionStore,
    SystemHealthStore,
    NodeListFieldsStore,
    CreateEnrollmentTokenStore,
    BootstrapTokenFieldsStore,
  } from "$houdini";
  import type { SystemHealth$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { auth } from "$lib/stores/auth";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import InfrastructureMetricCard from "$lib/components/shared/InfrastructureMetricCard.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { getIconComponent } from "$lib/iconUtils";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import {
    serviceInstanceRenderKey,
    sortServiceInstancesForRender,
  } from "$lib/utils/infrastructure-data";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";

  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const ServerIcon = getIconComponent("Server");
  const HardDriveIcon = getIconComponent("HardDrive");
  const ActivityIcon = getIconComponent("Activity");
  const PackageIcon = getIconComponent("Package");
  const CalendarIcon = getIconComponent("Calendar");
  const PlusIcon = getIconComponent("Plus");
  const XIcon = getIconComponent("X");
  const CopyIcon = getIconComponent("Copy");

  let clusterId = $derived(page.params.clusterId as string);

  const accessStore = new GetClustersAccessStore();
  const infrastructureStore = new GetInfrastructureOverviewStore();
  const networkStore = new GetNetworkStatusStore();
  const nodesStore = new GetNodesConnectionStore();
  const serviceInstancesStore = new GetServiceInstancesConnectionStore();
  const systemHealthSub = new SystemHealthStore();
  const nodeCoreStore = new NodeListFieldsStore();
  const createEnrollmentTokenMutation = new CreateEnrollmentTokenStore();
  const bootstrapTokenStore = new BootstrapTokenFieldsStore();

  let isAuthenticated = false;
  let accessDenied = $state(false);
  let systemHealthListening = false;
  let loadSequence = 0;
  let lastLoadedClusterId = $state<string | null>(null);
  let showEnrollmentModal = $state(false);
  let newEnrollmentName = $state("");
  let newEnrollmentTtl = $state("30d");
  let createdEnrollmentToken = $state<string | null>(null);
  let enrollmentSshTarget = $state("");

  let hasData = $derived(!!$infrastructureStore.data);
  let loading = $derived($infrastructureStore.fetching && !hasData);
  let creatingEnrollmentToken = $derived($createEnrollmentTokenMutation.fetching);

  let cluster = $derived(
    $infrastructureStore.data?.clustersConnection?.edges
      ?.map((e) => e.node)
      .find((c) => c.clusterId === clusterId) ?? null
  );

  let maskedNodes = $derived($nodesStore.data?.nodesConnection?.edges?.map((e) => e.node) ?? []);
  let nodes = $derived(maskedNodes.map((node) => get(fragment(node, nodeCoreStore))));
  let totalNodeCount = $derived($nodesStore.data?.nodesConnection?.totalCount ?? 0);

  function unmaskBootstrapToken(
    masked: { readonly " $fragments": { BootstrapTokenFields: object } } | null | undefined
  ) {
    if (!masked) return null;
    return get(fragment(masked, bootstrapTokenStore));
  }

  let serviceInstances = $derived(
    sortServiceInstancesForRender(
      $serviceInstancesStore.data?.analytics?.infra?.serviceInstancesConnection?.edges?.map(
        (e) => e.node
      ) ?? []
    )
  );

  let clusterAvgCpu = $derived.by(() => {
    const metrics = nodes
      .map((node) => node.liveState?.cpuPercent)
      .filter((cpu): cpu is number => typeof cpu === "number");
    if (metrics.length === 0) return 0;
    return metrics.reduce((sum, value) => sum + value, 0) / metrics.length;
  });

  let clusterAvgMemory = $derived.by(() => {
    const metrics = nodes
      .map((node) => {
        const max = node.liveState?.ramTotalBytes;
        const current = node.liveState?.ramUsedBytes;
        if (!max || max <= 0 || current == null) return null;
        return (current / max) * 100;
      })
      .filter((memory): memory is number => typeof memory === "number");
    if (metrics.length === 0) return 0;
    return metrics.reduce((sum, value) => sum + value, 0) / metrics.length;
  });

  // Live stats from periscope (via network overview)
  let clusterLiveStats = $derived(
    $networkStore.data?.networkStatus?.clusters?.find((c) => c.clusterId === clusterId) ?? null
  );
  let isMediaCluster = $derived(cluster?.clusterType === "edge");
  let currentStreams = $derived(clusterLiveStats?.currentStreams ?? 0);
  let currentViewers = $derived(clusterLiveStats?.currentViewers ?? 0);
  let egressMbps = $derived(clusterLiveStats?.egressMbps ?? 0);
  let ingressMbps = $derived(clusterLiveStats?.ingressMbps ?? 0);

  // Real-time system health
  type SystemHealthEvent = NonNullable<SystemHealth$result["liveSystemHealth"]>;
  let systemHealth = $state<Record<string, { event: SystemHealthEvent; ts: Date }>>({});
  const recentHeartbeatMs = 5 * 60 * 1000;

  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) =>
    ["24h", "7d", "30d"].includes(option.value)
  );

  type ClusterMetricCard = {
    key: string;
    label: string;
    subtitle: string;
    value: string | number;
    tone: string;
  };

  let metricCards = $derived.by(() => {
    const cards: ClusterMetricCard[] = [
      {
        key: "nodes",
        label: "Nodes",
        subtitle: "Registered to this cluster",
        value: totalNodeCount,
        tone: "text-primary",
      },
      {
        key: "services",
        label: "Services",
        subtitle: "Running service instances",
        value: serviceInstances.length,
        tone: "text-info",
      },
    ];

    if (isMediaCluster) {
      cards.push(
        {
          key: "streams",
          label: "Active Streams",
          subtitle: "Live right now",
          value: currentStreams,
          tone: "text-success",
        },
        {
          key: "viewers",
          label: "Active Viewers",
          subtitle: "Across active streams",
          value: currentViewers,
          tone: "text-accent-purple",
        },
        {
          key: "egress",
          label: "Egress",
          subtitle: "Delivery to viewers",
          value: `${egressMbps.toLocaleString()} Mbps`,
          tone: "text-warning",
        },
        {
          key: "ingress",
          label: "Ingress",
          subtitle: "Publisher + peer inbound",
          value: `${ingressMbps.toLocaleString()} Mbps`,
          tone: "text-warning",
        }
      );
    }

    cards.push(
      {
        key: "cpu",
        label: "Avg CPU",
        subtitle: "From current node live state",
        value: `${clusterAvgCpu.toFixed(1)}%`,
        tone:
          clusterAvgCpu < 70
            ? "text-success"
            : clusterAvgCpu < 90
              ? "text-warning"
              : "text-destructive",
      },
      {
        key: "memory",
        label: "Avg Memory",
        subtitle: "From current node live state",
        value: `${clusterAvgMemory.toFixed(1)}%`,
        tone:
          clusterAvgMemory < 70
            ? "text-success"
            : clusterAvgMemory < 90
              ? "text-warning"
              : "text-destructive",
      }
    );

    return cards;
  });

  const unsubscribeAuth = auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
  });

  onDestroy(() => {
    unsubscribeAuth();
    if (systemHealthListening) {
      systemHealthSub.unlisten();
    }
  });

  async function loadClusterData() {
    const requestId = ++loadSequence;
    accessDenied = false;
    try {
      await accessStore.fetch();
      const ownsCluster =
        $accessStore.data?.clustersAccess?.some(
          (entry) => entry.clusterId === clusterId && entry.accessLevel === "owner"
        ) ?? false;
      if (!ownsCluster) {
        accessDenied = true;
        if (systemHealthListening) {
          systemHealthSub.unlisten();
          systemHealthListening = false;
        }
        systemHealth = {};
        return;
      }
      if (!systemHealthListening) {
        systemHealthSub.listen();
        systemHealthListening = true;
      }

      await Promise.all([
        infrastructureStore.fetch(),
        networkStore.fetch(),
        nodesStore.fetch({ variables: { clusterId } }),
        serviceInstancesStore.fetch({ variables: { clusterId } }),
      ]);
      if (requestId !== loadSequence) return;

      if ($infrastructureStore.errors?.length) {
        console.error("Failed to load cluster data:", $infrastructureStore.errors);
        toast.error("Failed to load cluster data.");
      }

      // Initialize health from node liveState
      const initialHealth: Record<string, { event: SystemHealthEvent; ts: Date }> = {};
      for (const node of nodes) {
        if (!node?.nodeId) continue;
        const liveState = node?.liveState;
        if (liveState) {
          initialHealth[node.nodeId] = {
            event: {
              node: node.nodeId,
              location: liveState.location ?? "",
              status: liveState.isHealthy ? "HEALTHY" : "UNHEALTHY",
              cpuTenths: Math.round(liveState.cpuPercent * 10),
              isHealthy: liveState.isHealthy,
              ramMax: liveState.ramTotalBytes,
              ramCurrent: liveState.ramUsedBytes,
              diskTotalBytes: liveState.diskTotalBytes,
              diskUsedBytes: liveState.diskUsedBytes,
              shmTotalBytes: null,
              shmUsedBytes: null,
              timestamp: liveState.updatedAt,
            } as SystemHealthEvent,
            ts: new Date(liveState.updatedAt),
          };
        }
      }
      untrack(() => {
        systemHealth = initialHealth;
      });
    } catch (error) {
      console.error("Failed to load cluster data:", error);
      toast.error("Failed to load cluster data.");
    }
  }

  $effect(() => {
    if (!isAuthenticated || !clusterId || clusterId === lastLoadedClusterId) return;
    lastLoadedClusterId = clusterId;
    systemHealth = {};
    void loadClusterData();
  });

  // Handle real-time health updates for this cluster's nodes
  $effect(() => {
    const healthData = $systemHealthSub.data?.liveSystemHealth;
    if (healthData) {
      untrack(() => {
        const matchedNode = nodes.find(
          (n) =>
            (!!healthData.nodeId && n.id === healthData.nodeId) ||
            n.nodeId === healthData.node ||
            n.nodeName === healthData.node
        );
        const nodeKey = matchedNode?.nodeId;
        if (matchedNode && nodeKey) {
          systemHealth[nodeKey] = {
            event: healthData,
            ts: new Date(healthData.timestamp),
          };
          systemHealth = { ...systemHealth };
        }
      });
    }
  });

  function handleTimeRangeChange(value: string) {
    timeRange = value;
    loadClusterData();
  }

  async function createEnrollmentToken() {
    if (!clusterId) return;
    try {
      const result = await createEnrollmentTokenMutation.mutate({
        clusterId,
        name: newEnrollmentName.trim() || undefined,
        ttl: newEnrollmentTtl.trim() || undefined,
      });
      const data = result.data?.createEnrollmentToken;
      if (data?.__typename === "CreateEnrollmentTokenResponse") {
        const unmaskedToken = unmaskBootstrapToken(data.bootstrapToken);
        createdEnrollmentToken = unmaskedToken?.token ?? null;
        toast.success("Enrollment token created");
      } else if (data?.__typename === "ValidationError" || data?.__typename === "AuthError") {
        toast.error(data.message);
      } else {
        toast.error("Failed to create enrollment token");
      }
    } catch {
      toast.error("Failed to create enrollment token");
    }
  }

  function closeEnrollmentModal() {
    showEnrollmentModal = false;
    newEnrollmentName = "";
    newEnrollmentTtl = "30d";
    createdEnrollmentToken = null;
    enrollmentSshTarget = "";
  }

  let enrollmentDeployCommand = $derived(
    createdEnrollmentToken
      ? `frameworks edge deploy --ssh ${enrollmentSshTarget.trim() || "ubuntu@edge-1"} --enrollment-token ${createdEnrollmentToken}`
      : ""
  );

  function getStatusBadgeClass(status: string | null | undefined) {
    switch (status?.toLowerCase()) {
      case "healthy":
        return "border-success/40 bg-success/10 text-success";
      case "degraded":
        return "border-warning/40 bg-warning/10 text-warning";
      case "unhealthy":
        return "border-rose-500/40 bg-rose-500/10 text-rose-300";
      default:
        return "border-muted-foreground/40 bg-muted-foreground/10 text-muted-foreground";
    }
  }

  function hasRecentHeartbeat(node: { lastHeartbeat?: string | null } | null | undefined) {
    if (!node?.lastHeartbeat) return false;
    const timestamp = new Date(node.lastHeartbeat).getTime();
    if (Number.isNaN(timestamp)) return false;
    return Date.now() - timestamp <= recentHeartbeatMs;
  }

  function getNodeRecordStatus(node: {
    liveState?: { isHealthy: boolean } | null;
    lastHeartbeat?: string | null;
  }) {
    if (node.liveState?.isHealthy === true) return "Healthy";
    if (node.liveState?.isHealthy === false) return "Unhealthy";
    if (hasRecentHeartbeat(node)) return "Healthy";
    if (node.lastHeartbeat) return "Degraded";
    return "Unknown";
  }

  function formatServiceName(serviceId: string) {
    const serviceNames: Record<string, string> = {
      api_gateway: "Bridge",
      api_control: "Commodore",
      api_tenants: "Quartermaster",
      api_billing: "Purser",
      api_analytics_ingest: "Periscope Ingest",
      api_analytics_query: "Periscope Query",
      api_firehose: "Decklog",
      api_balancing: "Foghorn",
      api_sidecar: "Helmsman",
      api_realtime: "Signalman",
      api_forms: "Forms",
      api_dns: "Navigator",
      api_mesh: "Privateer",
    };
    return serviceNames[serviceId] || serviceId;
  }

  function formatTimeAgo(dateStr: string | null | undefined) {
    if (!dateStr) return "Never";
    const date = new Date(dateStr);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffSec = Math.floor(diffMs / 1000);
    if (diffSec < 60) return `${diffSec}s ago`;
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
    return `${Math.floor(diffSec / 86400)}d ago`;
  }
</script>

<svelte:head>
  <title>{cluster?.clusterName ?? "Cluster"} - Infrastructure - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center justify-between gap-4">
      <div class="flex items-center gap-3">
        <button
          onclick={() => goto(resolve("/infrastructure"))}
          class="text-muted-foreground hover:text-foreground transition-colors"
        >
          <ArrowLeftIcon class="w-5 h-5" />
        </button>
        <ServerIcon class="w-5 h-5 text-primary" />
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-xl font-bold text-foreground">
              {cluster?.clusterName ?? clusterId}
            </h1>
            {#if cluster}
              <Badge
                variant="outline"
                class="text-xs uppercase {getStatusBadgeClass(cluster.healthStatus)}"
              >
                {cluster.healthStatus}
              </Badge>
            {/if}
          </div>
          <p class="text-sm text-muted-foreground font-mono">{clusterId}</p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <Button
          variant="outline"
          class="gap-2"
          disabled={!cluster}
          onclick={() => (showEnrollmentModal = true)}
        >
          <PlusIcon class="w-4 h-4" />
          Add Edge
        </Button>
        <Select value={timeRange} onValueChange={handleTimeRangeChange} type="single">
          <SelectTrigger class="min-w-[150px]">
            <CalendarIcon class="w-4 h-4 mr-2 text-muted-foreground" />
            {currentRange.label}
          </SelectTrigger>
          <SelectContent>
            {#each timeRangeOptions as option (option.value)}
              <SelectItem value={option.value}>{option.label}</SelectItem>
            {/each}
          </SelectContent>
        </Select>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="dashboard-grid">
        <div class="slab col-span-full">
          <div class="slab-header">
            <SkeletonLoader type="text-lg" class="w-40" />
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
              {#each Array(6) as _, i (i)}
                <LoadingCard variant="infrastructure" />
              {/each}
            </div>
          </div>
        </div>
        <div class="slab col-span-full">
          <div class="slab-header">
            <SkeletonLoader type="text-lg" class="w-24" />
          </div>
          <div class="slab-body--padded">
            {#each Array(4) as _, i (i)}
              <SkeletonLoader type="text" class="w-full mb-2" />
            {/each}
          </div>
        </div>
      </div>
    {:else if accessDenied}
      <div class="p-8">
        <EmptyState
          iconName="Server"
          title="Cluster operator access required"
          description="Cluster inspection, nodes, services, and enrollment controls are available to cluster owners."
        />
      </div>
    {:else if !cluster}
      <div class="p-8">
        <EmptyState
          iconName="Server"
          title="Cluster not found"
          description="The cluster {clusterId} could not be found or you don't have access."
        />
      </div>
    {:else}
      <div class="dashboard-grid">
        <!-- Metric Cards -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ActivityIcon class="w-4 h-4 text-info" />
              <h3>Cluster Overview ({currentRange.label})</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
              {#each metricCards as stat (stat.key)}
                <InfrastructureMetricCard
                  label={stat.label}
                  subtitle={stat.subtitle}
                  value={stat.value}
                  tone={stat.tone}
                />
              {/each}
            </div>
          </div>
        </div>

        <!-- Cluster Details -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ServerIcon class="w-4 h-4 text-info" />
              <h3>Cluster Details</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
              <div class="space-y-1">
                <p class="text-muted-foreground">Type</p>
                <p class="font-medium">{cluster.clusterType}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">Deployment</p>
                <p class="font-medium">{cluster.deploymentModel}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">Visibility</p>
                <p class="font-medium capitalize">{cluster.visibility.toLowerCase()}</p>
              </div>
              <div class="space-y-1">
                <p class="text-muted-foreground">Created</p>
                <p class="font-medium">
                  {cluster.createdAt ? new Date(cluster.createdAt).toLocaleDateString() : "N/A"}
                </p>
              </div>
            </div>
          </div>
        </div>

        <!-- Nodes -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center justify-between w-full">
              <div class="flex items-center gap-2">
                <HardDriveIcon class="w-4 h-4 text-info" />
                <h3>Nodes</h3>
              </div>
              {#if totalNodeCount > 0}
                <Badge variant="outline" class="text-muted-foreground">
                  {totalNodeCount} node{totalNodeCount !== 1 ? "s" : ""}
                </Badge>
              {/if}
            </div>
            <p class="text-sm text-muted-foreground mt-1">
              Click a node to view detailed performance and configuration.
            </p>
          </div>
          <div class="slab-body--padded">
            {#if nodes.length === 0}
              <EmptyState
                iconName="HardDrive"
                title="No nodes"
                description="No nodes are registered to this cluster."
                size="sm"
                showAction={false}
              />
            {:else}
              <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                {#each nodes as node (node.id)}
                  {@const health = systemHealth[node.nodeId]}
                  {@const status = health?.event.status ?? getNodeRecordStatus(node)}
                  {@const cpuPercent = health ? (health.event.cpuTenths / 10).toFixed(1) : null}
                  {@const memPercent = health?.event.ramMax
                    ? (((health.event.ramCurrent || 0) / health.event.ramMax) * 100).toFixed(0)
                    : null}
                  <a href={resolve(`/nodes/${node.id}`)} class="block no-underline h-full">
                    <div
                      class="slab slab--compact h-full border-border/50 transition-colors hover:border-primary/50"
                    >
                      <div class="slab-header">
                        <div class="flex items-start justify-between gap-3 w-full">
                          <div>
                            <h4 class="font-semibold">{node.nodeName}</h4>
                            <p class="text-xs text-muted-foreground font-mono">{node.nodeId}</p>
                          </div>
                          <Badge
                            variant="outline"
                            class="text-[0.6rem] uppercase {getStatusBadgeClass(status)}"
                          >
                            {status}
                          </Badge>
                        </div>
                      </div>
                      <div class="slab-body--padded pt-5">
                        <div class="grid grid-cols-3 gap-2 text-xs">
                          <div>
                            <p class="text-muted-foreground">Type</p>
                            <p class="font-medium capitalize">{node.nodeType}</p>
                          </div>
                          <div>
                            <p class="text-muted-foreground">Region</p>
                            <p class="font-medium">{node.region ?? "—"}</p>
                          </div>
                          <div>
                            <p class="text-muted-foreground">Cores</p>
                            <p class="font-medium">{node.cpuCores ?? "—"}</p>
                          </div>
                        </div>
                        {#if cpuPercent || memPercent}
                          <div
                            class="mt-2 flex items-center gap-4 border-t border-border/30 pt-2 text-xs"
                          >
                            {#if cpuPercent}
                              <span>
                                <span class="text-muted-foreground">CPU</span>
                                <span
                                  class="ml-1 font-mono {Number(cpuPercent) > 80
                                    ? 'text-warning'
                                    : 'text-success'}">{cpuPercent}%</span
                                >
                              </span>
                            {/if}
                            {#if memPercent}
                              <span>
                                <span class="text-muted-foreground">RAM</span>
                                <span
                                  class="ml-1 font-mono {Number(memPercent) > 80
                                    ? 'text-warning'
                                    : 'text-success'}">{memPercent}%</span
                                >
                              </span>
                            {/if}
                          </div>
                        {/if}
                      </div>
                    </div>
                  </a>
                {/each}
              </div>
            {/if}
          </div>
        </div>

        <!-- Service Instances -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center justify-between w-full">
              <div class="flex items-center gap-2">
                <PackageIcon class="w-4 h-4 text-info" />
                <h3>Service Instances</h3>
              </div>
              {#if serviceInstances.length > 0}
                <Badge variant="outline" class="text-muted-foreground">
                  {serviceInstances.length} instance{serviceInstances.length !== 1 ? "s" : ""}
                </Badge>
              {/if}
            </div>
          </div>
          <div class="slab-body--padded">
            {#if serviceInstances.length === 0}
              <EmptyState
                iconName="Package"
                title="No service instances"
                description="No services are running in this cluster."
                size="sm"
                showAction={false}
              />
            {:else}
              <div class="space-y-2">
                {#each serviceInstances as instance (serviceInstanceRenderKey(instance))}
                  <div class="flex items-center justify-between p-3 border border-border/50">
                    <div class="flex items-center gap-3">
                      <div
                        class="w-2 h-2 {instance.healthStatus?.toLowerCase() === 'healthy'
                          ? 'bg-success'
                          : instance.healthStatus?.toLowerCase() === 'unhealthy'
                            ? 'bg-destructive'
                            : 'bg-muted-foreground'}"
                      ></div>
                      <div>
                        <p class="text-sm font-medium text-foreground">
                          {formatServiceName(instance.serviceId)}
                        </p>
                        <p class="text-xs text-muted-foreground">
                          {instance.instanceId}
                          {#if instance.version}
                            <span class="text-muted-foreground/60"> v{instance.version}</span>
                          {/if}
                        </p>
                      </div>
                    </div>
                    <div class="flex items-center gap-4 text-xs">
                      {#if instance.port}
                        <div class="text-right">
                          <span class="text-muted-foreground">Port</span>
                          <span class="ml-1 font-mono text-foreground">{instance.port}</span>
                        </div>
                      {/if}
                      <div class="text-right min-w-[60px]">
                        <span class="text-muted-foreground">Checked</span>
                        <span class="ml-1 font-mono text-foreground">
                          {formatTimeAgo(instance.lastHealthCheck)}
                        </span>
                      </div>
                      <Badge
                        variant="outline"
                        class="uppercase text-[0.6rem] {getStatusBadgeClass(instance.healthStatus)}"
                      >
                        {instance.healthStatus || "Unknown"}
                      </Badge>
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

{#if showEnrollmentModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center">
    <button
      type="button"
      class="absolute inset-0 bg-black/50 cursor-default"
      onclick={closeEnrollmentModal}
      aria-label="Close modal"
    ></button>
    <div
      class="relative bg-background border border-border rounded-lg shadow-xl max-w-md w-full mx-4 p-6"
    >
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-lg font-semibold">Add Edge</h2>
        <button onclick={closeEnrollmentModal} class="text-muted-foreground hover:text-foreground">
          <XIcon class="w-5 h-5" />
        </button>
      </div>

      {#if createdEnrollmentToken}
        <div class="space-y-4">
          <div class="p-4 bg-success/10 border border-success/20 rounded-lg">
            <p class="text-sm text-success font-medium mb-2">Enrollment token created.</p>
            <p class="text-xs text-muted-foreground mb-3">
              Copy the bootstrap token below. This is the only time it will be shown.
            </p>
            <div class="flex items-center gap-2">
              <code class="flex-1 p-2 bg-muted rounded text-xs font-mono break-all">
                {createdEnrollmentToken}
              </code>
              <Button
                variant="outline"
                size="sm"
                onclick={() => {
                  navigator.clipboard.writeText(createdEnrollmentToken!);
                  toast.success("Token copied to clipboard");
                }}
              >
                <CopyIcon class="w-4 h-4" />
              </Button>
            </div>
            <div class="mt-3">
              <label for="enrollmentSsh" class="block text-xs text-muted-foreground mb-1">
                SSH target
              </label>
              <input
                id="enrollmentSsh"
                type="text"
                bind:value={enrollmentSshTarget}
                placeholder="ubuntu@edge-1"
                class="w-full px-3 py-2 bg-muted border border-border rounded-md text-xs text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary"
              />
            </div>
            <div class="mt-3 flex items-center gap-2">
              <code class="flex-1 p-2 bg-muted rounded text-xs font-mono break-all">
                {enrollmentDeployCommand}
              </code>
              <Button
                variant="outline"
                size="sm"
                onclick={() => {
                  navigator.clipboard.writeText(enrollmentDeployCommand);
                  toast.success("Deploy command copied to clipboard");
                }}
              >
                <CopyIcon class="w-4 h-4" />
              </Button>
            </div>
          </div>
          <Button class="w-full" onclick={closeEnrollmentModal}>Done</Button>
        </div>
      {:else}
        <div class="space-y-4">
          <div>
            <label for="enrollmentName" class="block text-sm font-medium text-foreground mb-1">
              Token Name
            </label>
            <input
              id="enrollmentName"
              type="text"
              bind:value={newEnrollmentName}
              placeholder="e.g., Amsterdam edge 02"
              class="w-full px-3 py-2 bg-muted border border-border rounded-md text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary"
            />
          </div>
          <div>
            <label for="enrollmentTtl" class="block text-sm font-medium text-foreground mb-1">
              TTL
            </label>
            <Select bind:value={newEnrollmentTtl} type="single">
              <SelectTrigger id="enrollmentTtl" class="w-full">
                {newEnrollmentTtl}
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="24h">24h</SelectItem>
                <SelectItem value="7d">7d</SelectItem>
                <SelectItem value="30d">30d</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="flex gap-3">
            <Button variant="outline" class="flex-1" onclick={closeEnrollmentModal}>Cancel</Button>
            <Button
              class="flex-1"
              disabled={creatingEnrollmentToken}
              onclick={createEnrollmentToken}
            >
              {creatingEnrollmentToken ? "Creating..." : "Create Token"}
            </Button>
          </div>
        </div>
      {/if}
    </div>
  </div>
{/if}
