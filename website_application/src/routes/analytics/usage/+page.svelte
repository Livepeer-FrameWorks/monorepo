<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { resolve } from "$app/paths";
  import {
    fragment,
    GetUsageRecordsStore,
    GetBillingStatusStore,
    GetStorageUsageStore,
    GetViewerHoursHourlyStore,
    GetTenantAnalyticsDailyConnectionStore,
    BillingTierFieldsStore,
    UsageSummaryFieldsStore,
    AllocationFieldsStore
  } from "$houdini";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import { Alert, AlertTitle, AlertDescription } from "$lib/components/ui/alert";
  import {
    Select,
    SelectTrigger,
    SelectContent,
    SelectItem,
  } from "$lib/components/ui/select";
  import CodecDistributionChart from "$lib/components/charts/CodecDistributionChart.svelte";
  import StorageBreakdownChart from "$lib/components/charts/StorageBreakdownChart.svelte";
  import ViewerTrendChart from "$lib/components/charts/ViewerTrendChart.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getCountryName } from "$lib/utils/country-names";
  import { goto } from "$app/navigation";

  // Houdini stores
  const usageRecordsStore = new GetUsageRecordsStore();
  const billingStatusStore = new GetBillingStatusStore();
  const storageUsageStore = new GetStorageUsageStore();
  const viewerHoursHourlyStore = new GetViewerHoursHourlyStore();
  const tenantDailyStore = new GetTenantAnalyticsDailyConnectionStore();

  // Fragment stores for unmasking nested data
  const tierFragmentStore = new BillingTierFieldsStore();
  const usageSummaryFragmentStore = new UsageSummaryFieldsStore();
  const allocationFragmentStore = new AllocationFieldsStore();

  // Types from Houdini
  type UsageRecord = NonNullable<NonNullable<NonNullable<typeof $usageRecordsStore.data>["usageRecordsConnection"]>["edges"]>[0]["node"];

  let loading = $derived($usageRecordsStore.fetching || $billingStatusStore.fetching || $viewerHoursHourlyStore.fetching);
  let error = $state<string | null>(null);

  interface AggregatedUsage {
    stream_hours: number;
    egress_gb: number;
    recording_gb: number;
    peak_bandwidth_mbps: number;
    total_streams: number;
    total_viewers: number;
    peak_viewers: number;
    period: string;
  }

  let usageData = $state<AggregatedUsage>({
    stream_hours: 0,
    egress_gb: 0,
    recording_gb: 0,
    peak_bandwidth_mbps: 0,
    total_streams: 0,
    total_viewers: 0,
    peak_viewers: 0,
    period: "",
  });

  let billingData = $derived($billingStatusStore.data?.billingStatus ?? null);

  // Unmask fragment data for currentTier using get() pattern
  let currentTier = $derived(
    billingData?.currentTier
      ? get(fragment(billingData.currentTier, tierFragmentStore))
      : null
  );

  // Unmask fragment data for usageSummary using get() pattern
  let usageSummary = $derived(
    billingData?.usageSummary
      ? get(fragment(billingData.usageSummary, usageSummaryFragmentStore))
      : null
  );

  // Helper to unmask nested allocation fields
  function unmaskAllocation(masked: { readonly " $fragments": { AllocationFields: {} } } | null | undefined) {
    if (!masked) return null;
    return get(fragment(masked, allocationFragmentStore));
  }

  // Aggregate storage data from edges
  let storageData = $derived.by(() => {
    const edges = $storageUsageStore.data?.storageUsageConnection?.edges ?? [];
    if (edges.length === 0) return null;
    // Get the most recent snapshot or aggregate
    const latest = edges[0]?.node;
    if (!latest) return null;
    return {
      dvrBytes: latest.dvrBytes || 0,
      clipBytes: latest.clipBytes || 0,
      vodBytes: latest.vodBytes || 0,
      totalBytes: latest.totalBytes || 0,
    };
  });

  // Transform viewer hours hourly data for trend chart
  let viewerHoursTrendData = $derived.by(() => {
    const edges = $viewerHoursHourlyStore.data?.viewerHoursHourlyConnection?.edges ?? [];
    if (edges.length === 0) return [];

    // Aggregate by hour across all streams/countries
    const hourlyMap = new Map<string, number>();
    for (const edge of edges) {
      const node = edge.node;
      if (!node?.hour) continue;
      const hour = node.hour;
      const existing = hourlyMap.get(hour) || 0;
      hourlyMap.set(hour, existing + (node.uniqueViewers || 0));
    }

    return Array.from(hourlyMap.entries())
      .map(([hour, viewers]) => ({ timestamp: hour, viewers }))
      .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime());
  });

  // Transform tenant daily analytics for trend chart
  let tenantDailyTrendData = $derived.by(() => {
    const edges = $tenantDailyStore.data?.tenantAnalyticsDailyConnection?.edges ?? [];
    if (edges.length === 0) return [];

    return edges
      .map(edge => ({
        day: edge.node.day,
        totalStreams: edge.node.totalStreams,
        totalViews: edge.node.totalViews,
        uniqueViewers: edge.node.uniqueViewers,
        egressBytes: edge.node.egressBytes,
        egressGb: edge.node.egressBytes / (1024 * 1024 * 1024),
      }))
      .sort((a, b) => new Date(a.day).getTime() - new Date(b.day).getTime());
  });

  // Totals for tenant daily analytics
  let tenantDailyTotals = $derived.by(() => {
    return tenantDailyTrendData.reduce((acc, d) => ({
      streams: acc.streams + d.totalStreams,
      views: acc.views + d.totalViews,
      viewers: acc.viewers + d.uniqueViewers,
      egress: acc.egress + d.egressGb
    }), { streams: 0, views: 0, viewers: 0, egress: 0 });
  });

  let timeRange = $state("7d");
  const timeRangeLabels: Record<string, string> = {
    "7d": "Last 7 days",
    "30d": "Last 30 days",
    "90d": "Last 90 days",
  };
  let startTime = "";
  let endTime = "";

  onMount(async () => {
    await loadUsageAndCosts();
  });

  async function loadUsageAndCosts() {
    error = null;

    try {
      const now = new Date();
      const days = timeRange === "7d" ? 7 : timeRange === "30d" ? 30 : 90;
      startTime = new Date(now.getTime() - days * 24 * 60 * 60 * 1000).toISOString();
      endTime = now.toISOString();

      await Promise.all([
        usageRecordsStore.fetch({ variables: { timeRange: { start: startTime, end: endTime } } }),
        billingStatusStore.fetch(),
        storageUsageStore.fetch({ variables: { timeRange: { start: startTime, end: endTime }, first: 1 } }).catch(() => null),
        viewerHoursHourlyStore.fetch({ variables: { timeRange: { start: startTime, end: endTime }, first: 500 } }).catch(() => null),
        tenantDailyStore.fetch({ variables: { timeRange: { start: startTime, end: endTime }, first: 100 } }).catch(() => null),
      ]);

      const usageRecords = $usageRecordsStore.data?.usageRecordsConnection?.edges?.map(e => e.node) ?? [];

      if (usageRecords.length > 0) {
        const aggregated = usageRecords.reduce(
          (acc: AggregatedUsage, record: UsageRecord) => {
            switch (record.usageType) {
              case "stream_hours":
                acc.stream_hours += record.usageValue;
                break;
              case "egress_gb":
                acc.egress_gb += record.usageValue;
                break;
              case "recording_gb":
                acc.recording_gb += record.usageValue;
                break;
              case "peak_bandwidth_mbps":
                acc.peak_bandwidth_mbps = Math.max(acc.peak_bandwidth_mbps, record.usageValue);
                break;
              case "total_streams":
                acc.total_streams = Math.max(acc.total_streams, record.usageValue);
                break;
              case "peak_viewers":
                acc.peak_viewers = Math.max(acc.peak_viewers, record.usageValue);
                break;
            }
            return acc;
          },
          {
            stream_hours: 0,
            egress_gb: 0,
            recording_gb: 0,
            peak_bandwidth_mbps: 0,
            total_streams: 0,
            total_viewers: 0,
            peak_viewers: 0,
            period: `${days} days`,
          },
        );
        usageData = aggregated;
      }

      if ($usageRecordsStore.errors?.length || $billingStatusStore.errors?.length) {
        error = $usageRecordsStore.errors?.[0]?.message || $billingStatusStore.errors?.[0]?.message || "Failed to load data";
      }
    } catch (err: any) {
      error = err?.response?.data?.error || err?.message || "Failed to load usage and costs data";
      console.error("Failed to load usage and costs:", err);
    }
  }

  function calculateEstimatedCosts() {
    if (!currentTier?.basePrice) {
      return { total: 0, breakdown: { base: 0, bandwidth: 0, streaming: 0, storage: 0 } };
    }

    // Unmask nested allocation fields to get rates
    const bandwidthOverage = unmaskAllocation(currentTier.overageRates?.bandwidth);
    const storageOverage = unmaskAllocation(currentTier.overageRates?.storage);
    const computeAlloc = unmaskAllocation(currentTier.computeAllocation);

    const bandwidthRate = bandwidthOverage?.unitPrice ?? 0;
    const storageRate = storageOverage?.unitPrice ?? 0;
    const streamingRate = computeAlloc?.unitPrice ?? 0;

    const baseCost = currentTier.basePrice;
    const bandwidthCost = usageData.egress_gb * bandwidthRate;
    const streamingCost = usageData.stream_hours * streamingRate;
    const storageCost = usageData.recording_gb * storageRate;

    return {
      total: baseCost + bandwidthCost + streamingCost + storageCost,
      breakdown: { base: baseCost, bandwidth: bandwidthCost, streaming: streamingCost, storage: storageCost },
    };
  }

  let estimatedCosts = $derived.by(() => calculateEstimatedCosts());

  function formatCurrency(amount: number, currency = "USD") {
    return new Intl.NumberFormat("en-US", { style: "currency", currency }).format(amount);
  }

  function formatNumber(num: number) {
    return new Intl.NumberFormat().format(Math.round(num));
  }

  function formatBytes(bytes: number): string {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  }

  function handleTimeRangeChange(val: string) {
    timeRange = val;
    loadUsageAndCosts();
  }

  // Icons
  const RefreshIcon = getIconComponent("RefreshCw");
  const AlertCircleIcon = getIconComponent("AlertCircle");
  const ClockIcon = getIconComponent("Clock");
  const RadioIcon = getIconComponent("Radio");
  const UsersIcon = getIconComponent("Users");
  const VideoIcon = getIconComponent("Video");
  const HardDriveIcon = getIconComponent("HardDrive");
  const TrendingUpIcon = getIconComponent("TrendingUp");
  const LightbulbIcon = getIconComponent("Lightbulb");
  const GaugeIcon = getIconComponent("Gauge");
  const CreditCardIcon = getIconComponent("CreditCard");
  const ZapIcon = getIconComponent("Zap");
  const GlobeIcon = getIconComponent("Globe2");
  const CpuIcon = getIconComponent("Cpu");
  const ActivityIcon = getIconComponent("Activity");
  const CalendarIcon = getIconComponent("Calendar");

  function formatViewerHours(hours: number | null | undefined): string {
    if (hours == null) return '0';
    if (hours >= 1000) return `${(hours / 1000).toFixed(1)}k`;
    return hours.toFixed(1);
  }

  function formatProcessingTime(seconds: number | null | undefined): string {
    if (seconds == null || seconds === 0) return '0s';
    if (seconds < 60) return `${seconds.toFixed(1)}s`;
    const minutes = seconds / 60;
    if (minutes < 60) return `${minutes.toFixed(1)}m`;
    const hours = minutes / 60;
    return `${hours.toFixed(1)}h`;
  }

  function formatBillingMonth(yyyymm: string) {
    if (!yyyymm) return "";
    try {
      const [year, month] = yyyymm.split("-");
      const date = new Date(parseInt(year), parseInt(month) - 1, 1);
      return new Intl.DateTimeFormat("en-US", { month: "long", year: "numeric" }).format(date);
    } catch (e) {
      return yyyymm;
    }
  }
</script>

<svelte:head>
  <title>Usage & Costs - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <GaugeIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Usage & Costs</h1>
          <p class="text-sm text-muted-foreground">
            Track your streaming usage and what it's costing you
          </p>
        </div>
      </div>
      <div class="flex items-center gap-3">
        <Select value={timeRange} onValueChange={handleTimeRangeChange} type="single">
          <SelectTrigger class="min-w-[140px]">
            {timeRangeLabels[timeRange] ?? "Last 7 days"}
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="7d">Last 7 days</SelectItem>
            <SelectItem value="30d">Last 30 days</SelectItem>
            <SelectItem value="90d">Last 90 days</SelectItem>
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" onclick={loadUsageAndCosts}>
          <RefreshIcon class="w-4 h-4 mr-2" />
          Refresh
        </Button>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
  {#if loading}
    <div class="px-4 sm:px-6 lg:px-8 py-6">
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    </div>
  {:else if error}
    <div class="px-4 sm:px-6 lg:px-8 py-6">
      <div class="text-center py-12">
        <AlertCircleIcon class="w-6 h-6 text-destructive mx-auto mb-4" />
        <h3 class="text-lg font-semibold text-destructive mb-2">Error Loading Data</h3>
        <p class="text-muted-foreground mb-6">{error}</p>
        <Button onclick={loadUsageAndCosts}>Try Again</Button>
      </div>
    </div>
  {:else}
    <div class="page-transition">

      <!-- Usage Stats -->
      <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
        <div>
          <DashboardMetricCard
            icon={ClockIcon}
            iconColor="text-primary"
            value={formatNumber(usageData.stream_hours)}
            valueColor="text-primary"
            label="Stream Hours"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={RadioIcon}
            iconColor="text-success"
            value={`${formatNumber(usageData.egress_gb)} GB`}
            valueColor="text-success"
            label="Bandwidth Out"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={UsersIcon}
            iconColor="text-accent-purple"
            value={formatNumber(usageData.peak_viewers)}
            valueColor="text-accent-purple"
            label="Peak Viewers"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={VideoIcon}
            iconColor="text-info"
            value={formatNumber(usageData.total_streams)}
            valueColor="text-info"
            label="Total Streams"
          />
        </div>
      </GridSeam>

      <!-- Viewer Hours Trend Chart -->
      {#if viewerHoursTrendData.length > 0}
        <div class="slab mx-4 sm:mx-6 lg:mx-8 mt-4">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <TrendingUpIcon class="w-4 h-4 text-primary" />
              <h3>Viewer Trend (Hourly)</h3>
            </div>
            <span class="text-xs text-muted-foreground">{timeRangeLabels[timeRange]}</span>
          </div>
          <div class="slab-body--padded">
            <ViewerTrendChart data={viewerHoursTrendData} height={200} />
          </div>
        </div>
      {/if}

      <!-- Daily Usage Trend (from tenant_analytics_daily) -->
      {#if tenantDailyTrendData.length > 0}
        <div class="slab mx-4 sm:mx-6 lg:mx-8 mt-4">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <CalendarIcon class="w-4 h-4 text-info" />
              <h3>Daily Activity</h3>
            </div>
            <span class="text-xs text-muted-foreground">{timeRangeLabels[timeRange]}</span>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 sm:grid-cols-4 gap-4 mb-4">
              <div class="text-center p-2 border border-border/50 bg-background/50">
                <p class="text-[10px] text-muted-foreground uppercase">Total Streams</p>
                <p class="text-lg font-semibold text-info">{formatNumber(tenantDailyTotals.streams)}</p>
              </div>
              <div class="text-center p-2 border border-border/50 bg-background/50">
                <p class="text-[10px] text-muted-foreground uppercase">Total Views</p>
                <p class="text-lg font-semibold text-primary">{formatNumber(tenantDailyTotals.views)}</p>
              </div>
              <div class="text-center p-2 border border-border/50 bg-background/50">
                <p class="text-[10px] text-muted-foreground uppercase">Unique Viewers</p>
                <p class="text-lg font-semibold text-accent-purple">{formatNumber(tenantDailyTotals.viewers)}</p>
              </div>
              <div class="text-center p-2 border border-border/50 bg-background/50">
                <p class="text-[10px] text-muted-foreground uppercase">Egress (GB)</p>
                <p class="text-lg font-semibold text-success">{tenantDailyTotals.egress.toFixed(2)}</p>
              </div>
            </div>
            <!-- Daily breakdown table -->
            <div class="overflow-x-auto">
              <table class="w-full text-sm">
                <thead>
                  <tr class="border-b border-border/50 text-muted-foreground text-xs uppercase">
                    <th class="text-left py-2 px-2">Date</th>
                    <th class="text-right py-2 px-2">Streams</th>
                    <th class="text-right py-2 px-2">Views</th>
                    <th class="text-right py-2 px-2">Viewers</th>
                    <th class="text-right py-2 px-2">Egress</th>
                  </tr>
                </thead>
                <tbody>
                  {#each tenantDailyTrendData.slice().reverse() as day (day.day)}
                    <tr class="border-b border-border/30 hover:bg-muted/30">
                      <td class="py-2 px-2 font-mono text-muted-foreground">
                        {new Date(day.day).toLocaleDateString()}
                      </td>
                      <td class="text-right py-2 px-2 text-info">{day.totalStreams}</td>
                      <td class="text-right py-2 px-2 text-primary">{formatNumber(day.totalViews)}</td>
                      <td class="text-right py-2 px-2 text-accent-purple">{formatNumber(day.uniqueViewers)}</td>
                      <td class="text-right py-2 px-2 text-success">{day.egressGb.toFixed(2)} GB</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      {/if}

      <!-- Main Content Grid -->
      <div class="dashboard-grid">
        <!-- Your Plan Slab -->
        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <CreditCardIcon class="w-4 h-4 text-primary" />
              <h3>Your Plan</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="space-y-4">
              <div class="flex items-center justify-between">
                <span class="text-muted-foreground">Plan</span>
                <span class="font-semibold text-foreground">
                  {currentTier?.name || "Free"}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-muted-foreground">Monthly Cost</span>
                <span class="font-semibold text-success">
                  {currentTier?.basePrice
                    ? formatCurrency(currentTier.basePrice, currentTier.currency)
                    : "Free"}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-muted-foreground">Status</span>
                <span class="text-xs px-2 py-0.5 rounded-full bg-success/20 text-success capitalize">
                  {billingData?.billingStatus || "active"}
                </span>
              </div>
            </div>
          </div>
          <div class="slab-actions">
            <Button href={resolve("/account/billing")} variant="ghost" class="gap-2">
              <CreditCardIcon class="w-4 h-4" />
              Manage Billing
            </Button>
          </div>
        </div>

        <!-- Estimated Costs Slab -->
        <div class="slab">
          <div class="slab-header">
            <h3>What This Period Cost You</h3>
          </div>
          <div class="slab-body--padded">
            <div class="space-y-3">
              {#if estimatedCosts.breakdown.base > 0}
                <div class="flex items-center justify-between text-sm">
                  <span class="text-muted-foreground">Base plan</span>
                  <span class="text-foreground">{formatCurrency(estimatedCosts.breakdown.base)}</span>
                </div>
              {/if}
              {#if estimatedCosts.breakdown.streaming > 0}
                <div class="flex items-center justify-between text-sm">
                  <span class="text-muted-foreground">Streaming ({formatNumber(usageData.stream_hours)}h)</span>
                  <span class="text-foreground">{formatCurrency(estimatedCosts.breakdown.streaming)}</span>
                </div>
              {/if}
              {#if estimatedCosts.breakdown.bandwidth > 0}
                <div class="flex items-center justify-between text-sm">
                  <span class="text-muted-foreground">Bandwidth ({formatNumber(usageData.egress_gb)} GB)</span>
                  <span class="text-foreground">{formatCurrency(estimatedCosts.breakdown.bandwidth)}</span>
                </div>
              {/if}
              {#if estimatedCosts.breakdown.storage > 0}
                <div class="flex items-center justify-between text-sm">
                  <span class="text-muted-foreground">Storage ({formatNumber(usageData.recording_gb)} GB)</span>
                  <span class="text-foreground">{formatCurrency(estimatedCosts.breakdown.storage)}</span>
                </div>
              {/if}
              <div class="pt-3 border-t border-border/30">
                <div class="flex items-center justify-between">
                  <span class="text-foreground font-medium">Estimated Total</span>
                  <span class="text-success font-bold text-xl">
                    {formatCurrency(estimatedCosts.total)}
                  </span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Billing Period Engagement Slab -->
        {#if usageSummary}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <UsersIcon class="w-4 h-4 text-accent-purple" />
                <h3>Billing Period Engagement</h3>
              </div>
              <span class="text-xs text-muted-foreground font-medium bg-muted/50 px-2 py-1 rounded">
                Current period: {formatBillingMonth(usageSummary.billingMonth)}
              </span>
            </div>
            <div class="slab-body--padded">
              <div class="grid grid-cols-2 gap-x-6 gap-y-3 text-sm">
                <div class="flex items-center justify-between">
                  <span class="text-muted-foreground">Viewer Hours</span>
                  <span class="font-medium text-foreground">{formatViewerHours(usageSummary.viewerHours)}h</span>
                </div>
                <div class="flex items-center justify-between">
                  <span class="text-muted-foreground">Total Viewers</span>
                  <span class="font-medium text-foreground">{formatNumber(usageSummary.totalViewers)}</span>
                </div>
                <div class="flex items-center justify-between">
                  <span class="text-muted-foreground">Peak Viewers</span>
                  <span class="font-medium text-accent-purple">{formatNumber(usageSummary.peakViewers)}</span>
                </div>
                <div class="flex items-center justify-between">
                  <span class="text-muted-foreground">Max Viewers</span>
                  <span class="font-medium text-foreground">{formatNumber(usageSummary.maxViewers)}</span>
                </div>
                <div class="flex items-center justify-between">
                  <span class="text-muted-foreground">Avg Viewers</span>
                  <span class="font-medium text-foreground">{usageSummary.avgViewers?.toFixed(1) ?? '—'}</span>
                </div>
                <div class="flex items-center justify-between">
                  <span class="text-muted-foreground">Unique Users</span>
                  <span class="font-medium text-foreground">{formatNumber(usageSummary.uniqueUsers ?? 0)}</span>
                </div>
              </div>
            </div>
          </div>

          <!-- Processing Usage Slab -->
          {@const hasProcessingUsage = (usageSummary.livepeerSeconds ?? 0) > 0 || (usageSummary.nativeAvSeconds ?? 0) > 0}
          {#if hasProcessingUsage}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <CpuIcon class="w-4 h-4 text-tokyo-night-magenta" />
                <h3>Processing Usage</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-4 text-sm">
                <!-- Livepeer Gateway transcoding -->
                {#if (usageSummary.livepeerSeconds ?? 0) > 0}
                <div class="border-b border-border/30 pb-3">
                  <div class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2">
                    <span class="flex items-center gap-1">
                      <ZapIcon class="w-3 h-3" />
                      Livepeer Gateway
                    </span>
                  </div>
                  <div class="grid grid-cols-3 gap-4">
                    <div>
                      <div class="text-muted-foreground text-xs">Time</div>
                      <div class="font-medium text-tokyo-night-magenta">
                        {formatProcessingTime(usageSummary.livepeerSeconds)}
                      </div>
                    </div>
                    <div>
                      <div class="text-muted-foreground text-xs">Segments</div>
                      <div class="font-medium text-foreground">
                        {formatNumber(usageSummary.livepeerSegmentCount ?? 0)}
                      </div>
                    </div>
                    <div>
                      <div class="text-muted-foreground text-xs">Streams</div>
                      <div class="font-medium text-foreground">
                        {usageSummary.livepeerUniqueStreams ?? 0}
                      </div>
                    </div>
                  </div>
                </div>
                {/if}

                <!-- Native AV processing -->
                {#if (usageSummary.nativeAvSeconds ?? 0) > 0}
                <div>
                  <div class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2">
                    <span class="flex items-center gap-1">
                      <ActivityIcon class="w-3 h-3" />
                      Local Processing
                    </span>
                  </div>
                  <div class="grid grid-cols-3 gap-4">
                    <div>
                      <div class="text-muted-foreground text-xs">Time</div>
                      <div class="font-medium text-info">
                        {formatProcessingTime(usageSummary.nativeAvSeconds)}
                      </div>
                    </div>
                    <div>
                      <div class="text-muted-foreground text-xs">Segments</div>
                      <div class="font-medium text-foreground">
                        {formatNumber(usageSummary.nativeAvSegmentCount ?? 0)}
                      </div>
                    </div>
                    <div>
                      <div class="text-muted-foreground text-xs">Streams</div>
                      <div class="font-medium text-foreground">
                        {usageSummary.nativeAvUniqueStreams ?? 0}
                      </div>
                    </div>
                  </div>
                </div>
                {/if}
              </div>
            </div>
          </div>
          {/if}

          <!-- Storage Lifecycle Slab -->
          {@const hasStorageActivity = usageSummary.averageStorageGb ||
            usageSummary.clipsAdded || usageSummary.clipsDeleted ||
            usageSummary.dvrAdded || usageSummary.dvrDeleted ||
            usageSummary.vodAdded || usageSummary.vodDeleted}
          {#if hasStorageActivity}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <HardDriveIcon class="w-4 h-4 text-warning" />
                <h3>Storage Activity</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-4 text-sm">
                <!-- Average Storage -->
                {#if usageSummary.averageStorageGb}
                <div class="flex items-center justify-between">
                  <span class="text-muted-foreground">Avg Storage</span>
                  <span class="font-medium text-foreground">
                    {usageSummary.averageStorageGb?.toFixed(2) ?? '0'} GB
                  </span>
                </div>
                {/if}

                <!-- Clips Section -->
                {#if usageSummary.clipsAdded || usageSummary.clipsDeleted}
                <div class="border-t border-border/30 pt-3">
                  <div class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2">Clips</div>
                  <div class="space-y-2">
                    <div class="flex items-center justify-between">
                      <span class="text-muted-foreground">Added</span>
                      <span class="font-medium text-success">
                        +{usageSummary.clipsAdded ?? 0}
                        {#if usageSummary.clipStorageAddedGb}
                          <span class="text-xs text-muted-foreground ml-1">
                            ({usageSummary.clipStorageAddedGb.toFixed(2)} GB)
                          </span>
                        {/if}
                      </span>
                    </div>
                    <div class="flex items-center justify-between">
                      <span class="text-muted-foreground">Deleted</span>
                      <span class="font-medium text-destructive">
                        −{usageSummary.clipsDeleted ?? 0}
                        {#if usageSummary.clipStorageDeletedGb}
                          <span class="text-xs text-muted-foreground ml-1">
                            ({usageSummary.clipStorageDeletedGb.toFixed(2)} GB)
                          </span>
                        {/if}
                      </span>
                    </div>
                  </div>
                </div>
                {/if}

                <!-- DVR Section -->
                {#if usageSummary.dvrAdded || usageSummary.dvrDeleted}
                <div class="border-t border-border/30 pt-3">
                  <div class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2">DVR Recordings</div>
                  <div class="space-y-2">
                    <div class="flex items-center justify-between">
                      <span class="text-muted-foreground">Added</span>
                      <span class="font-medium text-success">
                        +{usageSummary.dvrAdded ?? 0}
                        {#if usageSummary.dvrStorageAddedGb}
                          <span class="text-xs text-muted-foreground ml-1">
                            ({usageSummary.dvrStorageAddedGb.toFixed(2)} GB)
                          </span>
                        {/if}
                      </span>
                    </div>
                    <div class="flex items-center justify-between">
                      <span class="text-muted-foreground">Deleted</span>
                      <span class="font-medium text-destructive">
                        −{usageSummary.dvrDeleted ?? 0}
                        {#if usageSummary.dvrStorageDeletedGb}
                          <span class="text-xs text-muted-foreground ml-1">
                            ({usageSummary.dvrStorageDeletedGb.toFixed(2)} GB)
                          </span>
                        {/if}
                      </span>
                    </div>
                  </div>
                </div>
                {/if}

                <!-- VOD Section -->
                {#if usageSummary.vodAdded || usageSummary.vodDeleted}
                <div class="border-t border-border/30 pt-3">
                  <div class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2">VOD Assets</div>
                  <div class="space-y-2">
                    <div class="flex items-center justify-between">
                      <span class="text-muted-foreground">Added</span>
                      <span class="font-medium text-success">
                        +{usageSummary.vodAdded ?? 0}
                        {#if usageSummary.vodStorageAddedGb}
                          <span class="text-xs text-muted-foreground ml-1">
                            ({usageSummary.vodStorageAddedGb.toFixed(2)} GB)
                          </span>
                        {/if}
                      </span>
                    </div>
                    <div class="flex items-center justify-between">
                      <span class="text-muted-foreground">Deleted</span>
                      <span class="font-medium text-destructive">
                        −{usageSummary.vodDeleted ?? 0}
                        {#if usageSummary.vodStorageDeletedGb}
                          <span class="text-xs text-muted-foreground ml-1">
                            ({usageSummary.vodStorageDeletedGb.toFixed(2)} GB)
                          </span>
                        {/if}
                      </span>
                    </div>
                  </div>
                </div>
                {/if}
              </div>
            </div>
          </div>
          {/if}
        {/if}

        <!-- Additional Metrics Slab -->
        {#if usageData.recording_gb > 0 || usageData.peak_bandwidth_mbps > 0}
          <div class="slab">
            <div class="slab-header">
              <h3>Additional Metrics</h3>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-4">
                {#if usageData.recording_gb > 0}
                  <div class="flex items-center justify-between p-3 border border-border/30 bg-muted/20">
                    <div class="flex items-center gap-3">
                      <HardDriveIcon class="w-5 h-5 text-warning" />
                      <span class="text-muted-foreground">Recording Storage</span>
                    </div>
                    <span class="font-bold text-warning">{formatNumber(usageData.recording_gb)} GB</span>
                  </div>
                {/if}
                {#if usageData.peak_bandwidth_mbps > 0}
                  <div class="flex items-center justify-between p-3 border border-border/30 bg-muted/20">
                    <div class="flex items-center gap-3">
                      <TrendingUpIcon class="w-5 h-5 text-destructive" />
                      <span class="text-muted-foreground">Peak Bandwidth</span>
                    </div>
                    <span class="font-bold text-destructive">{formatNumber(usageData.peak_bandwidth_mbps)} Mbps</span>
                  </div>
                {/if}
              </div>
            </div>
          </div>
        {/if}

        <!-- Storage Breakdown Slab -->
        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <HardDriveIcon class="w-4 h-4 text-warning" />
              <h3>Storage Breakdown</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            {#if storageData}
              <StorageBreakdownChart data={storageData} height={160} />
              <div class="mt-4 pt-4 border-t border-border/30">
                <div class="flex items-center justify-between text-sm">
                  <span class="text-muted-foreground">Total Storage</span>
                  <span class="font-bold text-foreground">
                    {formatBytes(storageData.totalBytes)}
                  </span>
                </div>
              </div>
            {:else}
              <EmptyState
                iconName="HardDrive"
                title="No storage data"
                description="Storage breakdown will appear when you have data."
                actionText="Manage Storage"
                onAction={() => goto(resolve("/analytics/storage"))}
              />
            {/if}
          </div>
        </div>

        <!-- Geographic Distribution Slab (Full Table) -->
        {#if usageSummary?.geoBreakdown && usageSummary.geoBreakdown.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <GlobeIcon class="w-4 h-4 text-tokyo-night-cyan" />
                <h3>Top Regions</h3>
              </div>
              <span class="text-xs text-muted-foreground">
                {usageSummary.uniqueCountries} countries, {usageSummary.uniqueCities} cities
              </span>
            </div>
            <div class="slab-body--flush overflow-x-auto">
              <table class="w-full text-sm">
                <thead>
                  <tr class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide">
                    <th class="text-left py-3 px-4">Country</th>
                    <th class="text-right py-3 px-4">Viewers</th>
                    <th class="text-right py-3 px-4">Watch Time</th>
                    <th class="text-right py-3 px-4">Bandwidth</th>
                    <th class="text-right py-3 px-4">Share</th>
                  </tr>
                </thead>
                <tbody>
                  {#each usageSummary.geoBreakdown as country (country.countryCode)}
                    <tr class="border-b border-border/30 hover:bg-muted/10">
                      <td class="py-3 px-4">
                        <span class="font-medium">{getCountryName(country.countryCode)}</span>
                        <span class="text-muted-foreground ml-1">({country.countryCode})</span>
                      </td>
                      <td class="py-3 px-4 text-right font-mono">
                        {country.viewerCount.toLocaleString()}
                      </td>
                      <td class="py-3 px-4 text-right font-mono">
                        {formatViewerHours(country.viewerHours)}h
                      </td>
                      <td class="py-3 px-4 text-right font-mono">
                        {country.egressGb.toFixed(1)} GB
                      </td>
                      <td class="py-3 px-4 text-right">
                        <div class="flex items-center justify-end gap-2">
                          <div class="w-16 h-1.5 bg-muted rounded-full overflow-hidden">
                            <div
                              class="h-full bg-info"
                              style="width: {Math.min(country.percentage || 0, 100)}%"
                            ></div>
                          </div>
                          <span class="font-mono text-xs w-12 text-right">{(country.percentage || 0).toFixed(1)}%</span>
                        </div>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
            <div class="slab-actions">
              <Button href={resolve("/analytics/geographic")} variant="ghost" class="gap-2">
                <GlobeIcon class="w-4 h-4" />
                Full Geographic Analytics
              </Button>
            </div>
          </div>
        {/if}

        <!-- Info Note -->
        <div class="col-span-full px-4 sm:px-6 lg:px-8 -mx-4 sm:-mx-6 lg:-mx-8">
          <Alert variant="warning">
            <LightbulbIcon class="w-4 h-4" />
            <AlertTitle>How We Calculate This</AlertTitle>
            <AlertDescription>
              Your usage data comes from real streaming activity, and costs are estimated based on your current plan.
              Your actual bill will match your subscription terms.
            </AlertDescription>
          </Alert>
        </div>
      </div>
    </div>
  {/if}
  </div>
</div>
