<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { resolve } from "$app/paths";
  import {
    fragment,
    GetUsageRecordsStore,
    GetUsageAggregatesStore,
    GetBillingStatusStore,
    GetStorageUsageStore,
    GetViewerHoursHourlyStore,
    GetTenantAnalyticsDailyConnectionStore,
    GetStreamAnalyticsDailyConnectionStore,
    GetProcessingUsageStore,
    GetAPIUsageConnectionStore,
    BillingTierFieldsStore,
    UsageSummaryFieldsStore,
    LiveUsageSummaryFieldsStore,
    AllocationFieldsStore,
  } from "$houdini";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import { Alert, AlertTitle, AlertDescription } from "$lib/components/ui/alert";
  import { Select, SelectTrigger, SelectContent, SelectItem } from "$lib/components/ui/select";
  import StorageBreakdownChart from "$lib/components/charts/StorageBreakdownChart.svelte";
  import ViewerTrendChart from "$lib/components/charts/ViewerTrendChart.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { getCountryName } from "$lib/utils/country-names";
  import { goto } from "$app/navigation";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";

  // Houdini stores
  const usageRecordsStore = new GetUsageRecordsStore();
  const usageAggregatesStore = new GetUsageAggregatesStore();
  const billingStatusStore = new GetBillingStatusStore();
  const storageUsageStore = new GetStorageUsageStore();
  const viewerHoursHourlyStore = new GetViewerHoursHourlyStore();
  const tenantDailyStore = new GetTenantAnalyticsDailyConnectionStore();
  const streamAnalyticsStore = new GetStreamAnalyticsDailyConnectionStore();
  const processingUsageStore = new GetProcessingUsageStore();
  const apiUsageStore = new GetAPIUsageConnectionStore();

  // Fragment stores for unmasking nested data
  const tierFragmentStore = new BillingTierFieldsStore();
  const usageSummaryFragmentStore = new UsageSummaryFieldsStore();
  const liveUsageFragmentStore = new LiveUsageSummaryFieldsStore();
  const allocationFragmentStore = new AllocationFieldsStore();

  // Types from Houdini
  type UsageRecord = NonNullable<
    NonNullable<NonNullable<typeof $usageRecordsStore.data>["usageRecordsConnection"]>["edges"]
  >[0]["node"];

  let loading = $derived(
    $usageRecordsStore.fetching ||
      $usageAggregatesStore.fetching ||
      $billingStatusStore.fetching ||
      $viewerHoursHourlyStore.fetching
  );
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

  interface UsageTrendPoint {
    timestamp: string | Date;
    viewers: number;
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
    billingData?.currentTier ? get(fragment(billingData.currentTier, tierFragmentStore)) : null
  );

  let invoicePreview = $derived(billingData?.invoicePreview ?? null);

  // Unmask fragment data for usageSummary using get() pattern (from invoice preview)
  let usageSummary = $derived(
    billingData?.invoicePreview?.usageSummary
      ? get(fragment(billingData.invoicePreview.usageSummary, usageSummaryFragmentStore))
      : null
  );

  // Calculate total viewers for geo breakdown percentage
  let geoTotalViewers = $derived(
    usageSummary?.geoBreakdown?.reduce((sum, c) => sum + c.viewerCount, 0) ?? 0
  );

  // Unmask live usage summary
  let liveUsage = $derived(
    billingData?.liveUsage ? get(fragment(billingData.liveUsage, liveUsageFragmentStore)) : null
  );

  // Helper to unmask nested allocation fields
  function unmaskAllocation(
    masked: { readonly " $fragments": { AllocationFields: object } } | null | undefined
  ) {
    if (!masked) return null;
    return get(fragment(masked, allocationFragmentStore));
  }

  // Aggregate storage data from edges
  let storageData = $derived.by(() => {
    const edges =
      $storageUsageStore.data?.analytics?.usage?.storage?.storageUsageConnection?.edges ?? [];
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

  // Processing events (detailed records) - collapsed by default
  let processingEventsExpanded = $state(false);
  let processingEvents = $derived(
    $processingUsageStore.data?.analytics?.usage?.processing?.processingUsageConnection?.edges?.map(
      (e) => e.node
    ) ?? []
  );
  let processingEventsTotalCount = $derived(
    $processingUsageStore.data?.analytics?.usage?.processing?.processingUsageConnection
      ?.totalCount ?? 0
  );

  // API usage data
  let apiUsageExpanded = $state(false);
  let apiUsageRecords = $derived(
    $apiUsageStore.data?.analytics?.usage?.api?.apiUsageConnection?.edges?.map((e) => e.node) ?? []
  );
  let apiUsageSummaries = $derived(
    $apiUsageStore.data?.analytics?.usage?.api?.apiUsageConnection?.summaries ?? []
  );
  let apiUsageOperationSummaries = $derived(
    $apiUsageStore.data?.analytics?.usage?.api?.apiUsageConnection?.operationSummaries ?? []
  );
  let apiUsageTotalCount = $derived(
    $apiUsageStore.data?.analytics?.usage?.api?.apiUsageConnection?.totalCount ?? 0
  );

  // Aggregate API usage by auth type
  let apiUsageByAuthType = $derived.by(() => {
    const summaries = apiUsageSummaries;
    const byAuth: Record<
      string,
      {
        requests: number;
        errors: number;
        avgDuration: number;
        complexity: number;
        users: number;
        tokens: number;
      }
    > = {};

    for (const s of summaries) {
      const existing = byAuth[s.authType] || {
        requests: 0,
        errors: 0,
        avgDuration: 0,
        complexity: 0,
        users: 0,
        tokens: 0,
      };
      existing.requests += s.totalRequests;
      existing.errors += s.totalErrors;
      existing.complexity += s.totalComplexity;
      existing.users = Math.max(existing.users, s.uniqueUsers);
      existing.tokens = Math.max(existing.tokens, s.uniqueTokens);
      byAuth[s.authType] = existing;
    }

    // Calculate weighted average duration
    for (const [auth, data] of Object.entries(byAuth)) {
      const authSummaries = summaries.filter((s) => s.authType === auth);
      const totalRequests = authSummaries.reduce((sum, s) => sum + s.totalRequests, 0);
      const weightedDuration = authSummaries.reduce(
        (sum, s) => sum + s.avgDurationMs * s.totalRequests,
        0
      );
      data.avgDuration = totalRequests > 0 ? weightedDuration / totalRequests : 0;
    }

    return Object.entries(byAuth).map(([authType, data]) => ({
      authType,
      ...data,
    }));
  });

  // Aggregate API usage by operation type
  let apiUsageByOpType = $derived.by(() => {
    const summaries = apiUsageOperationSummaries;
    return summaries.map((s) => ({
      opType: s.operationType,
      requests: s.totalRequests,
      errors: s.totalErrors,
      uniqueOperations: s.uniqueOperations,
    }));
  });

  // Total API requests
  let apiTotalRequests = $derived(apiUsageByAuthType.reduce((sum, a) => sum + a.requests, 0));
  let apiTotalErrors = $derived(apiUsageByAuthType.reduce((sum, a) => sum + a.errors, 0));

  // Transform viewer hours hourly data for trend chart
  let viewerTrendData = $derived.by(() => {
    if (useHourlyTrend) {
      const edges =
        $viewerHoursHourlyStore.data?.analytics?.usage?.streaming?.viewerHoursHourlyConnection
          ?.edges ?? [];
      if (edges.length === 0) return [];

      // Aggregate by hour across all streams/countries
      const hourlyMap: Record<string, number> = {};
      for (const edge of edges) {
        const node = edge.node;
        if (!node?.hour) continue;
        const hour = node.hour;
        const existing = hourlyMap[hour] || 0;
        hourlyMap[hour] = existing + (node.uniqueViewers || 0);
      }

      return Object.entries(hourlyMap)
        .map(([hour, viewers]) => ({ timestamp: hour, viewers }))
        .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime());
    }

    return tenantDailyTrendData
      .map((d) => ({ timestamp: d.day, viewers: d.uniqueViewers }))
      .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime());
  });

  // Transform tenant daily analytics for trend chart
  let tenantDailyTrendData = $derived.by(() => {
    const edges =
      $tenantDailyStore.data?.analytics?.usage?.streaming?.tenantAnalyticsDailyConnection?.edges ??
      [];
    if (edges.length === 0) return [];

    return edges
      .map((edge) => ({
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
    return tenantDailyTrendData.reduce(
      (acc, d) => ({
        streams: acc.streams + d.totalStreams,
        views: acc.views + d.totalViews,
        viewers: acc.viewers + d.uniqueViewers,
        egress: acc.egress + d.egressGb,
      }),
      { streams: 0, views: 0, viewers: 0, egress: 0 }
    );
  });

  // Top streams by usage (cost attribution)
  let topStreamsByUsage = $derived.by(() => {
    const edges =
      $streamAnalyticsStore.data?.analytics?.usage?.streaming?.streamAnalyticsDailyConnection
        ?.edges ?? [];
    if (edges.length === 0) return [];

    // Aggregate by stream
    const streamMap: Record<
      string,
      {
        streamId: string;
        displayStreamId: string;
        totalViews: number;
        uniqueViewers: number;
        egressBytes: number;
        uniqueCountries: number;
        uniqueCities: number;
      }
    > = {};

    for (const edge of edges) {
      const node = edge.node;
      if (!node?.streamId) continue;

      const displayStreamId = node.stream?.streamId ?? node.streamId;
      const existing = streamMap[node.streamId];
      if (existing) {
        existing.totalViews += node.totalViews ?? 0;
        existing.uniqueViewers = Math.max(existing.uniqueViewers, node.uniqueViewers ?? 0);
        existing.egressBytes += node.egressBytes ?? 0;
        existing.uniqueCountries = Math.max(existing.uniqueCountries, node.uniqueCountries ?? 0);
        existing.uniqueCities = Math.max(existing.uniqueCities, node.uniqueCities ?? 0);
        if (!existing.displayStreamId) {
          existing.displayStreamId = displayStreamId;
        }
      } else {
        streamMap[node.streamId] = {
          streamId: node.streamId,
          displayStreamId,
          totalViews: node.totalViews ?? 0,
          uniqueViewers: node.uniqueViewers ?? 0,
          egressBytes: node.egressBytes ?? 0,
          uniqueCountries: node.uniqueCountries ?? 0,
          uniqueCities: node.uniqueCities ?? 0,
        };
      }
    }

    // Sort by egress (cost driver) and take top 10
    const totalEgress = Object.values(streamMap).reduce((sum, s) => sum + s.egressBytes, 0);
    return Object.values(streamMap)
      .map((s) => ({
        ...s,
        egressGb: s.egressBytes / (1024 * 1024 * 1024),
        percentage: totalEgress > 0 ? (s.egressBytes / totalEgress) * 100 : 0,
      }))
      .sort((a, b) => b.egressBytes - a.egressBytes)
      .slice(0, 10);
  });

  let usageAggregateSeries = $derived.by(() => {
    const aggregates = $usageAggregatesStore.data?.usageAggregates ?? [];

    const buildSeries = (usageType: string): UsageTrendPoint[] => {
      return aggregates
        .filter((entry) => entry.usageType === usageType)
        .map((entry) => ({
          timestamp: entry.periodStart ?? entry.periodEnd ?? "",
          viewers: entry.usageValue ?? 0,
        }))
        .filter((entry) => entry.timestamp)
        .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime());
    };

    return {
      streamHours: buildSeries("stream_hours"),
      egressGb: buildSeries("egress_gb"),
      recordingGb: buildSeries("recording_gb"),
    };
  });

  let timeRange = $state("7d");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS.filter((option) =>
    ["24h", "7d", "30d", "90d"].includes(option.value)
  );
  const useHourlyTrend = $derived(currentRange.days <= 7);

  onMount(async () => {
    await loadUsageAndCosts();
  });

  async function loadUsageAndCosts() {
    error = null;

    try {
      const range = resolveTimeRange(timeRange);
      const shouldUseHourly = range.days <= 7;
      const aggregateGranularity =
        range.days <= 7 ? "hourly" : range.days <= 90 ? "daily" : "monthly";
      currentRange = range;

      await Promise.all([
        usageRecordsStore.fetch({
          variables: { timeRange: { start: range.start, end: range.end } },
        }),
        usageAggregatesStore.fetch({
          variables: {
            timeRange: { start: range.start, end: range.end },
            granularity: aggregateGranularity,
            usageTypes: [
              "stream_hours",
              "egress_gb",
              "recording_gb",
              "peak_bandwidth_mbps",
              "total_streams",
              "total_viewers",
              "peak_viewers",
            ],
          },
        }),
        billingStatusStore.fetch(),
        storageUsageStore
          .fetch({ variables: { timeRange: { start: range.start, end: range.end }, first: 1 } })
          .catch(() => null),
        shouldUseHourly
          ? viewerHoursHourlyStore
              .fetch({
                variables: { timeRange: { start: range.start, end: range.end }, first: 500 },
              })
              .catch(() => null)
          : Promise.resolve(),
        tenantDailyStore
          .fetch({
            variables: {
              timeRange: { start: range.start, end: range.end },
              first: Math.min(range.days, 120),
            },
          })
          .catch(() => null),
        streamAnalyticsStore
          .fetch({ variables: { timeRange: { start: range.start, end: range.end }, first: 500 } })
          .catch(() => null),
        processingUsageStore
          .fetch({ variables: { timeRange: { start: range.start, end: range.end }, first: 50 } })
          .catch(() => null),
        apiUsageStore
          .fetch({ variables: { timeRange: { start: range.start, end: range.end }, first: 100 } })
          .catch(() => null),
      ]);

      const aggregates = $usageAggregatesStore.data?.usageAggregates ?? [];
      const usageRecords =
        $usageRecordsStore.data?.usageRecordsConnection?.edges?.map((e) => e.node) ?? [];

      if (aggregates.length > 0) {
        const aggregated = aggregates.reduce(
          (acc: AggregatedUsage, entry) => {
            switch (entry.usageType) {
              case "stream_hours":
                acc.stream_hours += entry.usageValue;
                break;
              case "egress_gb":
                acc.egress_gb += entry.usageValue;
                break;
              case "recording_gb":
                acc.recording_gb += entry.usageValue;
                break;
              case "peak_bandwidth_mbps":
                acc.peak_bandwidth_mbps = Math.max(acc.peak_bandwidth_mbps, entry.usageValue);
                break;
              case "total_streams":
                acc.total_streams = Math.max(acc.total_streams, entry.usageValue);
                break;
              case "peak_viewers":
                acc.peak_viewers = Math.max(acc.peak_viewers, entry.usageValue);
                break;
              case "total_viewers":
                acc.total_viewers = Math.max(acc.total_viewers, entry.usageValue);
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
            period: `${range.days} days`,
          }
        );
        usageData = aggregated;
      } else if (usageRecords.length > 0) {
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
            period: `${range.days} days`,
          }
        );
        usageData = aggregated;
      }

      if (
        $usageRecordsStore.errors?.length ||
        $usageAggregatesStore.errors?.length ||
        $billingStatusStore.errors?.length
      ) {
        error =
          $usageRecordsStore.errors?.[0]?.message ||
          $usageAggregatesStore.errors?.[0]?.message ||
          $billingStatusStore.errors?.[0]?.message ||
          "Failed to load data";
      }
    } catch (err: unknown) {
      const errObj = err as { response?: { data?: { error?: string } }; message?: string } | null;
      error =
        errObj?.response?.data?.error || errObj?.message || "Failed to load usage and costs data";
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
      breakdown: {
        base: baseCost,
        bandwidth: bandwidthCost,
        streaming: streamingCost,
        storage: storageCost,
      },
    };
  }

  let estimatedCosts = $derived.by(() => calculateEstimatedCosts());

  function formatCurrency(amount: number, currency = "USD") {
    return new Intl.NumberFormat("en-US", { style: "currency", currency }).format(amount);
  }

  function formatNumber(num: number) {
    return new Intl.NumberFormat().format(Math.round(num));
  }

  function formatTimestamp(value: string | null | undefined) {
    if (!value) return "—";
    return new Date(value).toLocaleString();
  }

  let recentUsageRecords = $derived(
    $usageRecordsStore.data?.usageRecordsConnection?.edges?.map((e) => e.node) ?? []
  );

  let usageRecordPreview = $derived(recentUsageRecords.slice(0, 20));

  function formatUsageValue(value: number | null | undefined) {
    if (value == null || Number.isNaN(value)) return "0";
    if (Number.isInteger(value)) return formatNumber(value);
    if (Math.abs(value) >= 1000) return formatNumber(value);
    return value.toFixed(2);
  }

  function formatUsagePeriod(record: UsageRecord | null | undefined) {
    if (!record) return "—";
    const start = record.periodStart ?? record.createdAt;
    const end = record.periodEnd ?? record.createdAt;
    if (!start && !end) return "—";
    if (start && end && start !== end) {
      return `${formatTimestamp(start)} – ${formatTimestamp(end)}`;
    }
    return formatTimestamp(start ?? end);
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
  const CodeIcon = getIconComponent("Code");
  const KeyIcon = getIconComponent("Key");
  const WalletIcon = getIconComponent("Wallet");

  function formatViewerHours(hours: number | null | undefined): string {
    if (hours == null) return "0";
    if (hours >= 1000) return `${(hours / 1000).toFixed(1)}k`;
    return hours.toFixed(1);
  }

  function formatProcessingTime(seconds: number | null | undefined): string {
    if (seconds == null || seconds === 0) return "0s";
    if (seconds < 60) return `${seconds.toFixed(1)}s`;
    const minutes = seconds / 60;
    if (minutes < 60) return `${minutes.toFixed(1)}m`;
    const hours = minutes / 60;
    return `${hours.toFixed(1)}h`;
  }

  function toDate(value: Date | string | null | undefined): Date | null {
    if (!value) return null;
    if (value instanceof Date) return value;
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) return null;
    return parsed;
  }

  function formatPeriodRange(
    start: Date | string | null | undefined,
    end: Date | string | null | undefined
  ) {
    const startDate = toDate(start);
    const endDate = toDate(end);
    if (!startDate || !endDate) return "";
    const fmt = new Intl.DateTimeFormat("en-US", {
      month: "short",
      day: "numeric",
      year: "numeric",
    });
    return `${fmt.format(startDate)} – ${fmt.format(endDate)}`;
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
            {currentRange.label}
          </SelectTrigger>
          <SelectContent>
            {#each timeRangeOptions as option (option.value)}
              <SelectItem value={option.value}>{option.label}</SelectItem>
            {/each}
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

        <!-- Usage Trends -->
        {#if usageAggregateSeries.streamHours.length > 0 || usageAggregateSeries.egressGb.length > 0 || usageAggregateSeries.recordingGb.length > 0}
          <div class="slab mx-4 sm:mx-6 lg:mx-8 mt-4">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <TrendingUpIcon class="w-4 h-4 text-primary" />
                <h3>Usage Trends</h3>
              </div>
              <span class="text-xs text-muted-foreground">{currentRange.label}</span>
            </div>
            <div class="slab-body--padded">
              <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                {#if usageAggregateSeries.streamHours.length > 0}
                  <div class="border border-border/30 bg-muted/10 p-3">
                    <div class="text-xs text-muted-foreground mb-2">Stream Hours</div>
                    <ViewerTrendChart
                      data={usageAggregateSeries.streamHours}
                      height={180}
                      seriesLabel="Stream Hours"
                      valueFormatter={(value) => `${value.toFixed(1)} h`}
                    />
                  </div>
                {/if}
                {#if usageAggregateSeries.egressGb.length > 0}
                  <div class="border border-border/30 bg-muted/10 p-3">
                    <div class="text-xs text-muted-foreground mb-2">Bandwidth (GB)</div>
                    <ViewerTrendChart
                      data={usageAggregateSeries.egressGb}
                      height={180}
                      seriesLabel="Bandwidth (GB)"
                      valueFormatter={(value) => `${value.toFixed(1)} GB`}
                    />
                  </div>
                {/if}
                {#if usageAggregateSeries.recordingGb.length > 0}
                  <div class="border border-border/30 bg-muted/10 p-3">
                    <div class="text-xs text-muted-foreground mb-2">Recording Storage (GB)</div>
                    <ViewerTrendChart
                      data={usageAggregateSeries.recordingGb}
                      height={180}
                      seriesLabel="Recording (GB)"
                      valueFormatter={(value) => `${value.toFixed(1)} GB`}
                    />
                  </div>
                {/if}
              </div>
            </div>
          </div>
        {/if}

        <!-- Viewer Hours Trend Chart -->
        {#if viewerTrendData.length > 0}
          <div class="slab mx-4 sm:mx-6 lg:mx-8 mt-4">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <TrendingUpIcon class="w-4 h-4 text-primary" />
                <h3>{useHourlyTrend ? "Viewer Trend (Hourly)" : "Viewer Trend (Daily)"}</h3>
              </div>
              <span class="text-xs text-muted-foreground">{currentRange.label}</span>
            </div>
            <div class="slab-body--padded">
              <ViewerTrendChart data={viewerTrendData} height={200} />
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
              <span class="text-xs text-muted-foreground">{currentRange.label}</span>
            </div>
            <div class="slab-body--padded">
              <div class="grid grid-cols-2 sm:grid-cols-4 gap-4 mb-4">
                <div class="text-center p-2 border border-border/50 bg-background/50">
                  <p class="text-[10px] text-muted-foreground uppercase">Total Streams</p>
                  <p class="text-lg font-semibold text-info">
                    {formatNumber(tenantDailyTotals.streams)}
                  </p>
                </div>
                <div class="text-center p-2 border border-border/50 bg-background/50">
                  <p class="text-[10px] text-muted-foreground uppercase">Total Views</p>
                  <p class="text-lg font-semibold text-primary">
                    {formatNumber(tenantDailyTotals.views)}
                  </p>
                </div>
                <div class="text-center p-2 border border-border/50 bg-background/50">
                  <p class="text-[10px] text-muted-foreground uppercase">Unique Viewers</p>
                  <p class="text-lg font-semibold text-accent-purple">
                    {formatNumber(tenantDailyTotals.viewers)}
                  </p>
                </div>
                <div class="text-center p-2 border border-border/50 bg-background/50">
                  <p class="text-[10px] text-muted-foreground uppercase">Egress (GB)</p>
                  <p class="text-lg font-semibold text-success">
                    {tenantDailyTotals.egress.toFixed(2)}
                  </p>
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
                    {#each tenantDailyTrendData.slice().reverse() as day, i (`${day.day}-${i}`)}
                      <tr class="border-b border-border/30 hover:bg-muted/30">
                        <td class="py-2 px-2 font-mono text-muted-foreground">
                          {new Date(day.day).toLocaleDateString()}
                        </td>
                        <td class="text-right py-2 px-2 text-info">{day.totalStreams}</td>
                        <td class="text-right py-2 px-2 text-primary"
                          >{formatNumber(day.totalViews)}</td
                        >
                        <td class="text-right py-2 px-2 text-accent-purple"
                          >{formatNumber(day.uniqueViewers)}</td
                        >
                        <td class="text-right py-2 px-2 text-success"
                          >{day.egressGb.toFixed(2)} GB</td
                        >
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
                    {currentTier?.displayName || "Free"}
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
                  <span
                    class="text-xs px-2 py-0.5 rounded-full bg-success/20 text-success capitalize"
                  >
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
                    <span class="text-foreground"
                      >{formatCurrency(estimatedCosts.breakdown.base)}</span
                    >
                  </div>
                {/if}
                {#if estimatedCosts.breakdown.streaming > 0}
                  <div class="flex items-center justify-between text-sm">
                    <span class="text-muted-foreground"
                      >Streaming ({formatNumber(usageData.stream_hours)}h)</span
                    >
                    <span class="text-foreground"
                      >{formatCurrency(estimatedCosts.breakdown.streaming)}</span
                    >
                  </div>
                {/if}
                {#if estimatedCosts.breakdown.bandwidth > 0}
                  <div class="flex items-center justify-between text-sm">
                    <span class="text-muted-foreground"
                      >Bandwidth ({formatNumber(usageData.egress_gb)} GB)</span
                    >
                    <span class="text-foreground"
                      >{formatCurrency(estimatedCosts.breakdown.bandwidth)}</span
                    >
                  </div>
                {/if}
                {#if estimatedCosts.breakdown.storage > 0}
                  <div class="flex items-center justify-between text-sm">
                    <span class="text-muted-foreground"
                      >Storage ({formatNumber(usageData.recording_gb)} GB)</span
                    >
                    <span class="text-foreground"
                      >{formatCurrency(estimatedCosts.breakdown.storage)}</span
                    >
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
                <span
                  class="text-xs text-muted-foreground font-medium bg-muted/50 px-2 py-1 rounded"
                >
                  Current period: {formatPeriodRange(
                    invoicePreview?.periodStart ?? liveUsage?.periodStart,
                    invoicePreview?.periodEnd ?? liveUsage?.periodEnd
                  )}
                </span>
                {#if billingData && billingData.usageReconciled === false}
                  <span class="text-xs text-warning">Live usage syncing…</span>
                {/if}
              </div>
              <div class="slab-body--padded">
                <div class="grid grid-cols-2 gap-x-6 gap-y-3 text-sm">
                  <div class="flex items-center justify-between">
                    <span class="text-muted-foreground">Viewer Hours</span>
                    <span class="font-medium text-foreground"
                      >{formatViewerHours(
                        liveUsage?.viewerHours ?? usageSummary.viewerHours
                      )}h</span
                    >
                  </div>
                  <div class="flex items-center justify-between">
                    <span class="text-muted-foreground">Total Viewers</span>
                    <span class="font-medium text-foreground"
                      >{formatNumber(usageSummary.totalViewers)}</span
                    >
                  </div>
                  <div class="flex items-center justify-between">
                    <span class="text-muted-foreground">Peak Viewers</span>
                    <span class="font-medium text-accent-purple"
                      >{formatNumber(usageSummary.peakViewers)}</span
                    >
                  </div>
                  <div class="flex items-center justify-between">
                    <span class="text-muted-foreground">Max Viewers</span>
                    <span class="font-medium text-foreground"
                      >{formatNumber(usageSummary.maxViewers)}</span
                    >
                  </div>
                  <div class="flex items-center justify-between">
                    <span class="text-muted-foreground">Avg Viewers</span>
                    <span class="font-medium text-foreground"
                      >{usageSummary.avgViewers?.toFixed(1) ?? "—"}</span
                    >
                  </div>
                  <div class="flex items-center justify-between">
                    <span class="text-muted-foreground">Unique Users (Period)</span>
                    <span class="font-medium text-foreground">
                      {formatNumber(
                        liveUsage?.uniqueViewers ??
                          usageSummary.uniqueUsersPeriod ??
                          usageSummary.uniqueUsers ??
                          0
                      )}
                    </span>
                  </div>
                </div>
              </div>
            </div>

            <!-- Processing Usage Slab -->
            {@const livepeerSeconds =
              liveUsage?.livepeerSeconds ?? usageSummary.livepeerSeconds ?? 0}
            {@const nativeAvSeconds =
              liveUsage?.nativeAvSeconds ?? usageSummary.nativeAvSeconds ?? 0}
            {@const hasProcessingUsage = livepeerSeconds > 0 || nativeAvSeconds > 0}
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
                    {#if livepeerSeconds > 0}
                      <div class="border-b border-border/30 pb-3">
                        <div
                          class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2"
                        >
                          <span class="flex items-center gap-1">
                            <ZapIcon class="w-3 h-3" />
                            Livepeer Gateway
                          </span>
                        </div>
                        <div class="grid grid-cols-3 gap-4">
                          <div>
                            <div class="text-muted-foreground text-xs">Time</div>
                            <div class="font-medium text-tokyo-night-magenta">
                              {formatProcessingTime(livepeerSeconds)}
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
                    {#if nativeAvSeconds > 0}
                      <div>
                        <div
                          class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2"
                        >
                          <span class="flex items-center gap-1">
                            <ActivityIcon class="w-3 h-3" />
                            Local Processing
                          </span>
                        </div>
                        <div class="grid grid-cols-3 gap-4">
                          <div>
                            <div class="text-muted-foreground text-xs">Time</div>
                            <div class="font-medium text-info">
                              {formatProcessingTime(nativeAvSeconds)}
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

                    <!-- Per-codec breakdown -->
                    {#if usageSummary}
                      {@const codecData = [
                        {
                          codec: "H.264",
                          seconds:
                            (usageSummary.livepeerH264Seconds ?? 0) +
                            (usageSummary.nativeAvH264Seconds ?? 0),
                          color: "bg-blue-500",
                        },
                        {
                          codec: "VP9",
                          seconds:
                            (usageSummary.livepeerVp9Seconds ?? 0) +
                            (usageSummary.nativeAvVp9Seconds ?? 0),
                          color: "bg-purple-500",
                        },
                        {
                          codec: "AV1",
                          seconds:
                            (usageSummary.livepeerAv1Seconds ?? 0) +
                            (usageSummary.nativeAvAv1Seconds ?? 0),
                          color: "bg-green-500",
                        },
                        {
                          codec: "HEVC",
                          seconds:
                            (usageSummary.livepeerHevcSeconds ?? 0) +
                            (usageSummary.nativeAvHevcSeconds ?? 0),
                          color: "bg-orange-500",
                        },
                        {
                          codec: "AAC",
                          seconds: usageSummary.nativeAvAacSeconds ?? 0,
                          color: "bg-pink-500",
                        },
                        {
                          codec: "Opus",
                          seconds: usageSummary.nativeAvOpusSeconds ?? 0,
                          color: "bg-cyan-500",
                        },
                      ].filter((c) => c.seconds > 0)}
                      {@const codecTotal = codecData.reduce((sum, c) => sum + c.seconds, 0)}
                      {#if codecData.length > 0}
                        <div class="border-t border-border/30 pt-3 mt-3">
                          <div
                            class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-3"
                          >
                            Codec Distribution
                          </div>
                          <div class="space-y-2">
                            {#each codecData as codec (codec.codec)}
                              {@const pct = codecTotal > 0 ? (codec.seconds / codecTotal) * 100 : 0}
                              <div class="flex items-center gap-3">
                                <span class="text-xs text-muted-foreground w-12">{codec.codec}</span
                                >
                                <div class="flex-1 h-2 bg-muted rounded-full overflow-hidden">
                                  <div class="{codec.color} h-full" style="width: {pct}%"></div>
                                </div>
                                <span class="text-xs font-mono w-16 text-right"
                                  >{formatProcessingTime(codec.seconds)}</span
                                >
                                <span class="text-xs text-muted-foreground w-12 text-right"
                                  >{pct.toFixed(1)}%</span
                                >
                              </div>
                            {/each}
                          </div>
                        </div>
                      {/if}
                    {/if}
                  </div>
                </div>
              </div>
            {/if}

            <!-- Processing Events Detail (Collapsed by default) -->
            {#if processingEvents.length > 0}
              <div class="slab col-span-full">
                <div class="slab-header">
                  <button
                    class="flex items-center gap-2 w-full text-left"
                    onclick={() => (processingEventsExpanded = !processingEventsExpanded)}
                  >
                    <CpuIcon class="w-4 h-4 text-muted-foreground" />
                    <h3>Processing Events Detail</h3>
                    <span class="text-xs text-muted-foreground ml-2">
                      ({processingEventsTotalCount} records)
                    </span>
                    <span class="ml-auto text-xs text-muted-foreground">
                      {processingEventsExpanded ? "▼" : "▶"}
                    </span>
                  </button>
                </div>
                {#if processingEventsExpanded}
                  <div class="slab-body--flush overflow-x-auto max-h-96">
                    <table class="w-full text-sm">
                      <thead class="sticky top-0 bg-background">
                        <tr
                          class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide"
                        >
                          <th class="text-left py-2 px-4">Time</th>
                          <th class="text-left py-2 px-4">Type</th>
                          <th class="text-left py-2 px-4">Stream</th>
                          <th class="text-left py-2 px-4">Input</th>
                          <th class="text-left py-2 px-4">Output</th>
                          <th class="text-right py-2 px-4">Duration</th>
                          <th class="text-right py-2 px-4">RTF</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each processingEvents as evt (evt.id)}
                          {@const displayStreamId = evt.stream?.streamId ?? evt.streamId}
                          <tr class="border-b border-border/30 hover:bg-muted/10">
                            <td class="py-2 px-4 text-xs text-muted-foreground">
                              {new Date(evt.timestamp).toLocaleTimeString()}
                            </td>
                            <td class="py-2 px-4">
                              <span
                                class="px-1.5 py-0.5 rounded text-[10px] font-mono {evt.processType ===
                                'livepeer_gateway'
                                  ? 'bg-tokyo-night-magenta/20 text-tokyo-night-magenta'
                                  : 'bg-info/20 text-info'}"
                              >
                                {evt.processType === "livepeer_gateway" ? "LP" : "Local"}
                              </span>
                            </td>
                            <td class="py-2 px-4">
                              <a
                                href={resolve(`/streams/${evt.streamId}`)}
                                class="font-mono text-xs text-primary hover:underline"
                              >
                                {displayStreamId?.slice(0, 8)}...
                              </a>
                            </td>
                            <td class="py-2 px-4 text-xs">
                              <span class="text-muted-foreground">{evt.inputCodec || "-"}</span>
                              {#if evt.width && evt.height}
                                <span class="text-muted-foreground ml-1"
                                  >({evt.width}×{evt.height})</span
                                >
                              {/if}
                            </td>
                            <td class="py-2 px-4 text-xs">
                              <span class="text-foreground">{evt.outputCodec || "-"}</span>
                              {#if evt.outputWidth && evt.outputHeight}
                                <span class="text-muted-foreground ml-1"
                                  >({evt.outputWidth}×{evt.outputHeight})</span
                                >
                              {/if}
                            </td>
                            <td class="py-2 px-4 text-right font-mono text-xs">
                              {evt.durationMs ? `${(evt.durationMs / 1000).toFixed(1)}s` : "-"}
                            </td>
                            <td class="py-2 px-4 text-right">
                              {#if evt.rtfOut}
                                <span
                                  class="font-mono text-xs {evt.rtfOut < 1
                                    ? 'text-success'
                                    : evt.rtfOut < 2
                                      ? 'text-warning'
                                      : 'text-destructive'}"
                                >
                                  {evt.rtfOut.toFixed(2)}x
                                </span>
                              {:else}
                                <span class="text-muted-foreground text-xs">-</span>
                              {/if}
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                {:else}
                  <div class="slab-body--padded text-sm text-muted-foreground">
                    Click to expand detailed processing event log
                  </div>
                {/if}
              </div>
            {/if}

            <!-- API Usage Slab -->
            {#if apiTotalRequests > 0 || apiUsageRecords.length > 0}
              <div class="slab">
                <div class="slab-header">
                  <div class="flex items-center gap-2">
                    <CodeIcon class="w-4 h-4 text-tokyo-night-cyan" />
                    <h3>API Usage</h3>
                  </div>
                  <span class="text-xs text-muted-foreground">
                    {formatNumber(apiTotalRequests)} requests
                  </span>
                </div>
                <div class="slab-body--padded">
                  <div class="space-y-4 text-sm">
                    <!-- Summary Stats -->
                    <div class="grid grid-cols-3 gap-4">
                      <div class="text-center p-2 border border-border/50 bg-background/50">
                        <p class="text-[10px] text-muted-foreground uppercase">Total Requests</p>
                        <p class="text-lg font-semibold text-primary">
                          {formatNumber(apiTotalRequests)}
                        </p>
                      </div>
                      <div class="text-center p-2 border border-border/50 bg-background/50">
                        <p class="text-[10px] text-muted-foreground uppercase">Errors</p>
                        <p
                          class="text-lg font-semibold {apiTotalErrors > 0
                            ? 'text-destructive'
                            : 'text-success'}"
                        >
                          {formatNumber(apiTotalErrors)}
                        </p>
                      </div>
                      <div class="text-center p-2 border border-border/50 bg-background/50">
                        <p class="text-[10px] text-muted-foreground uppercase">Error Rate</p>
                        <p
                          class="text-lg font-semibold {apiTotalRequests > 0 &&
                          apiTotalErrors / apiTotalRequests > 0.05
                            ? 'text-warning'
                            : 'text-success'}"
                        >
                          {apiTotalRequests > 0
                            ? ((apiTotalErrors / apiTotalRequests) * 100).toFixed(2)
                            : "0"}%
                        </p>
                      </div>
                    </div>

                    <!-- By Auth Type -->
                    {#if apiUsageByAuthType.length > 0}
                      <div class="border-t border-border/30 pt-3">
                        <div
                          class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2"
                        >
                          By Authentication
                        </div>
                        <div class="space-y-2">
                          {#each apiUsageByAuthType as auth (auth.authType)}
                            {@const pct =
                              apiTotalRequests > 0 ? (auth.requests / apiTotalRequests) * 100 : 0}
                            <div class="flex items-center gap-3">
                              <span class="flex items-center gap-1.5 text-xs w-20">
                                {#if auth.authType === "jwt"}
                                  <UsersIcon class="w-3 h-3 text-primary" />
                                {:else if auth.authType === "api_token"}
                                  <KeyIcon class="w-3 h-3 text-warning" />
                                {:else if auth.authType === "wallet"}
                                  <WalletIcon class="w-3 h-3 text-accent-purple" />
                                {:else}
                                  <CodeIcon class="w-3 h-3 text-muted-foreground" />
                                {/if}
                                <span class="capitalize">{auth.authType.replace("_", " ")}</span>
                              </span>
                              <div class="flex-1 h-2 bg-muted rounded-full overflow-hidden">
                                <div
                                  class="h-full {auth.authType === 'jwt'
                                    ? 'bg-primary'
                                    : auth.authType === 'api_token'
                                      ? 'bg-warning'
                                      : 'bg-accent-purple'}"
                                  style="width: {pct}%"
                                ></div>
                              </div>
                              <span class="text-xs font-mono w-20 text-right"
                                >{formatNumber(auth.requests)}</span
                              >
                              <span class="text-xs text-muted-foreground w-12 text-right"
                                >{pct.toFixed(1)}%</span
                              >
                            </div>
                          {/each}
                        </div>
                      </div>
                    {/if}

                    <!-- By Operation Type -->
                    {#if apiUsageByOpType.length > 0}
                      <div class="border-t border-border/30 pt-3">
                        <div
                          class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2"
                        >
                          By Operation Type
                        </div>
                        <div class="grid grid-cols-3 gap-3">
                          {#each apiUsageByOpType as op (op.opType)}
                            <div class="p-2 border border-border/30 bg-muted/10">
                              <div class="text-xs text-muted-foreground capitalize">
                                {op.opType}
                              </div>
                              <div class="text-lg font-semibold text-foreground">
                                {formatNumber(op.requests)}
                              </div>
                              <div class="text-[10px] text-muted-foreground">
                                {op.uniqueOperations} unique ops
                              </div>
                            </div>
                          {/each}
                        </div>
                      </div>
                    {/if}
                  </div>
                </div>
              </div>

              <!-- API Usage Details (Expandable) -->
              {#if apiUsageRecords.length > 0}
                <div class="slab col-span-full">
                  <div class="slab-header">
                    <button
                      class="flex items-center gap-2 w-full text-left"
                      onclick={() => (apiUsageExpanded = !apiUsageExpanded)}
                    >
                      <CodeIcon class="w-4 h-4 text-muted-foreground" />
                      <h3>API Request Details</h3>
                      <span class="text-xs text-muted-foreground ml-2">
                        ({apiUsageTotalCount} records)
                      </span>
                      <span class="ml-auto text-xs text-muted-foreground">
                        {apiUsageExpanded ? "▼" : "▶"}
                      </span>
                    </button>
                  </div>
                  {#if apiUsageExpanded}
                    <div class="slab-body--flush overflow-x-auto max-h-96">
                      <table class="w-full text-sm">
                        <thead class="sticky top-0 bg-background">
                          <tr
                            class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide"
                          >
                            <th class="text-left py-2 px-4">Time</th>
                            <th class="text-left py-2 px-4">Auth</th>
                            <th class="text-left py-2 px-4">Type</th>
                            <th class="text-left py-2 px-4">Operation</th>
                            <th class="text-right py-2 px-4">Requests</th>
                            <th class="text-right py-2 px-4">Errors</th>
                            <th class="text-right py-2 px-4">Avg Duration</th>
                          </tr>
                        </thead>
                        <tbody>
                          {#each apiUsageRecords as record (record.id)}
                            {@const avgDuration =
                              record.requestCount > 0
                                ? record.totalDurationMs / record.requestCount
                                : 0}
                            <tr class="border-b border-border/30 hover:bg-muted/10">
                              <td class="py-2 px-4 text-xs text-muted-foreground">
                                {new Date(record.timestamp).toLocaleString()}
                              </td>
                              <td class="py-2 px-4">
                                <span
                                  class="px-1.5 py-0.5 rounded text-[10px] font-mono {record.authType ===
                                  'jwt'
                                    ? 'bg-primary/20 text-primary'
                                    : record.authType === 'api_token'
                                      ? 'bg-warning/20 text-warning'
                                      : 'bg-accent-purple/20 text-accent-purple'}"
                                >
                                  {record.authType}
                                </span>
                              </td>
                              <td class="py-2 px-4">
                                <span
                                  class="px-1.5 py-0.5 rounded text-[10px] font-mono {record.operationType ===
                                  'query'
                                    ? 'bg-info/20 text-info'
                                    : record.operationType === 'mutation'
                                      ? 'bg-success/20 text-success'
                                      : 'bg-tokyo-night-magenta/20 text-tokyo-night-magenta'}"
                                >
                                  {record.operationType}
                                </span>
                              </td>
                              <td class="py-2 px-4 font-mono text-xs">
                                {record.operationName}
                              </td>
                              <td class="py-2 px-4 text-right font-mono">
                                {formatNumber(record.requestCount)}
                              </td>
                              <td
                                class="py-2 px-4 text-right font-mono {record.errorCount > 0
                                  ? 'text-destructive'
                                  : 'text-muted-foreground'}"
                              >
                                {record.errorCount}
                              </td>
                              <td class="py-2 px-4 text-right font-mono text-xs">
                                {avgDuration.toFixed(1)}ms
                              </td>
                            </tr>
                          {/each}
                        </tbody>
                      </table>
                    </div>
                  {:else}
                    <div class="slab-body--padded text-sm text-muted-foreground">
                      Click to expand detailed API usage log
                    </div>
                  {/if}
                </div>
              {/if}
            {/if}

            <!-- Storage Lifecycle Slab -->
            {@const hasStorageActivity =
              usageSummary.averageStorageGb ||
              usageSummary.clipsAdded ||
              usageSummary.clipsDeleted ||
              usageSummary.dvrAdded ||
              usageSummary.dvrDeleted ||
              usageSummary.vodAdded ||
              usageSummary.vodDeleted}
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
                          {usageSummary.averageStorageGb?.toFixed(2) ?? "0"} GB
                        </span>
                      </div>
                    {/if}

                    <!-- Clips Section -->
                    {#if usageSummary.clipsAdded || usageSummary.clipsDeleted}
                      <div class="border-t border-border/30 pt-3">
                        <div
                          class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2"
                        >
                          Clips
                        </div>
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
                        <div
                          class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2"
                        >
                          DVR Recordings
                        </div>
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
                        <div
                          class="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2"
                        >
                          VOD Assets
                        </div>
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
                    <div
                      class="flex items-center justify-between p-3 border border-border/30 bg-muted/20"
                    >
                      <div class="flex items-center gap-3">
                        <HardDriveIcon class="w-5 h-5 text-warning" />
                        <span class="text-muted-foreground">Recording Storage</span>
                      </div>
                      <span class="font-bold text-warning"
                        >{formatNumber(usageData.recording_gb)} GB</span
                      >
                    </div>
                  {/if}
                  {#if usageData.peak_bandwidth_mbps > 0}
                    <div
                      class="flex items-center justify-between p-3 border border-border/30 bg-muted/20"
                    >
                      <div class="flex items-center gap-3">
                        <TrendingUpIcon class="w-5 h-5 text-destructive" />
                        <span class="text-muted-foreground">Peak Bandwidth</span>
                      </div>
                      <span class="font-bold text-destructive"
                        >{formatNumber(usageData.peak_bandwidth_mbps)} Mbps</span
                      >
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

          <!-- Latest Usage Records Slab -->
          {#if usageRecordPreview.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <ActivityIcon class="w-4 h-4 text-primary" />
                  <h3>Latest Usage Records</h3>
                </div>
                <span class="text-xs text-muted-foreground">
                  Showing {usageRecordPreview.length} of {recentUsageRecords.length}
                </span>
              </div>
              <div class="slab-body--flush overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr
                      class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide"
                    >
                      <th class="text-left py-3 px-4">Type</th>
                      <th class="text-right py-3 px-4">Value</th>
                      <th class="text-right py-3 px-4">Granularity</th>
                      <th class="text-right py-3 px-4">Period</th>
                      <th class="text-right py-3 px-4">Recorded</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each usageRecordPreview as record (record.id)}
                      <tr class="border-b border-border/30 hover:bg-muted/10">
                        <td class="py-3 px-4 font-mono text-muted-foreground">
                          {record.usageType}
                        </td>
                        <td class="py-3 px-4 text-right font-mono">
                          {formatUsageValue(record.usageValue)}
                        </td>
                        <td class="py-3 px-4 text-right">
                          {record.granularity ?? "—"}
                        </td>
                        <td class="py-3 px-4 text-right text-muted-foreground">
                          {formatUsagePeriod(record)}
                        </td>
                        <td class="py-3 px-4 text-right text-muted-foreground">
                          {formatTimestamp(record.createdAt)}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          {/if}

          <!-- Top Streams by Usage (Cost Attribution) -->
          {#if topStreamsByUsage.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <TrendingUpIcon class="w-4 h-4 text-warning" />
                  <h3>Top Streams by Usage</h3>
                </div>
                <span class="text-xs text-muted-foreground">
                  {topStreamsByUsage.length} streams driving costs
                </span>
              </div>
              <div class="slab-body--flush overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr
                      class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide"
                    >
                      <th class="text-left py-3 px-4">Stream</th>
                      <th class="text-right py-3 px-4">Views</th>
                      <th class="text-right py-3 px-4">Viewers</th>
                      <th class="text-right py-3 px-4">Bandwidth</th>
                      <th class="text-right py-3 px-4">Cost Share</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each topStreamsByUsage as stream (stream.streamId)}
                      {@const displayStreamId = stream.displayStreamId}
                      <tr class="border-b border-border/30 hover:bg-muted/10">
                        <td class="py-3 px-4">
                          <a
                            href={resolve(`/streams/${stream.streamId}`)}
                            class="group flex items-center gap-2"
                          >
                            <span class="font-mono text-xs text-primary group-hover:underline">
                              {displayStreamId}
                            </span>
                            <span
                              class="text-[10px] text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity"
                            >
                              →
                            </span>
                          </a>
                        </td>
                        <td class="py-3 px-4 text-right font-mono">
                          {stream.totalViews.toLocaleString()}
                        </td>
                        <td class="py-3 px-4 text-right font-mono">
                          {stream.uniqueViewers.toLocaleString()}
                        </td>
                        <td class="py-3 px-4 text-right font-mono">
                          {stream.egressGb.toFixed(2)} GB
                        </td>
                        <td class="py-3 px-4 text-right">
                          <div class="flex items-center justify-end gap-2">
                            <div class="w-16 h-1.5 bg-muted rounded-full overflow-hidden">
                              <div
                                class="h-full bg-warning"
                                style="width: {Math.min(stream.percentage, 100)}%"
                              ></div>
                            </div>
                            <span class="font-mono text-xs w-12 text-right"
                              >{stream.percentage.toFixed(1)}%</span
                            >
                          </div>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          {/if}

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
                    <tr
                      class="border-b border-border/50 text-muted-foreground text-xs uppercase tracking-wide"
                    >
                      <th class="text-left py-3 px-4">Country</th>
                      <th class="text-right py-3 px-4">Viewers</th>
                      <th class="text-right py-3 px-4">Watch Time</th>
                      <th class="text-right py-3 px-4">Bandwidth</th>
                      <th class="text-right py-3 px-4">Share</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each usageSummary.geoBreakdown as country (country.countryCode)}
                      {@const pct =
                        geoTotalViewers > 0 ? (country.viewerCount / geoTotalViewers) * 100 : 0}
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
                                style="width: {Math.min(pct, 100)}%"
                              ></div>
                            </div>
                            <span class="font-mono text-xs w-12 text-right">{pct.toFixed(1)}%</span>
                          </div>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
              <div class="slab-actions">
                <Button href={resolve("/analytics/audience")} variant="ghost" class="gap-2">
                  <GlobeIcon class="w-4 h-4" />
                  Full Audience Analytics
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
                Your usage data comes from real streaming activity, and costs are estimated based on
                your current plan. Your actual bill will match your subscription terms.
              </AlertDescription>
            </Alert>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>
