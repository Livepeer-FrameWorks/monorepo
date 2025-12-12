<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import {
    GetUsageRecordsStore,
    GetBillingStatusStore,
    GetStorageUsageStore
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
  import { getCountryName } from "$lib/utils/country-names";

  // Houdini stores
  const usageRecordsStore = new GetUsageRecordsStore();
  const billingStatusStore = new GetBillingStatusStore();
  const storageUsageStore = new GetStorageUsageStore();

  // Types from Houdini
  type UsageRecord = NonNullable<NonNullable<typeof $usageRecordsStore.data>["usageRecords"]>[0];

  let loading = $derived($usageRecordsStore.fetching || $billingStatusStore.fetching);
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
      recordingBytes: latest.recordingBytes || 0,
      totalBytes: latest.totalBytes || 0,
    };
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
      ]);

      const usageRecords = $usageRecordsStore.data?.usageRecords ?? [];

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
    if (!billingData?.currentTier?.basePrice) {
      return { total: 0, breakdown: { base: 0, bandwidth: 0, streaming: 0, storage: 0 } };
    }

    const tier = billingData.currentTier;
    const bandwidthRate = tier.overageRates?.bandwidth?.unitPrice ?? 0;
    const storageRate = tier.overageRates?.storage?.unitPrice ?? 0;
    const streamingRate = tier.computeAllocation?.unitPrice ?? 0;

    const baseCost = tier.basePrice;
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
  const ChartLineIcon = getIconComponent("ChartLine");
  const ZapIcon = getIconComponent("Zap");
  const GlobeIcon = getIconComponent("Globe2");

  function formatViewerHours(hours: number): string {
    if (hours >= 1000) return `${(hours / 1000).toFixed(1)}k`;
    return hours.toFixed(1);
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
                  {billingData?.currentTier?.name || "Free"}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-muted-foreground">Monthly Cost</span>
                <span class="font-semibold text-success">
                  {billingData?.currentTier?.basePrice
                    ? formatCurrency(billingData.currentTier.basePrice, billingData.currentTier.currency)
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
        {#if billingData?.usageSummary}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <UsersIcon class="w-4 h-4 text-accent-purple" />
                <h3>Billing Period Engagement</h3>
              </div>
              <span class="text-xs text-muted-foreground font-medium bg-muted/50 px-2 py-1 rounded">
                Current period: {formatBillingMonth(billingData.usageSummary.billingMonth)}
              </span>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-3">
                <div class="flex items-center justify-between text-sm">
                  <span class="text-muted-foreground">Viewer Hours</span>
                  <span class="text-foreground">{formatNumber(billingData.usageSummary.viewerHours)} h</span>
                </div>
                <div class="flex items-center justify-between text-sm">
                  <span class="text-muted-foreground">Peak Viewers</span>
                  <span class="text-foreground">{formatNumber(billingData.usageSummary.peakViewers)}</span>
                </div>
              </div>
            </div>
          </div>
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
            <StorageBreakdownChart data={storageData} height={160} />
            {#if storageData}
              <div class="mt-4 pt-4 border-t border-border/30">
                <div class="flex items-center justify-between text-sm">
                  <span class="text-muted-foreground">Total Storage</span>
                  <span class="font-bold text-foreground">
                    {formatBytes(storageData.totalBytes)}
                  </span>
                </div>
              </div>
            {/if}
          </div>
        </div>

        <!-- Geographic Distribution Slab (Full Table) -->
        {#if billingData?.usageSummary?.geoBreakdown && billingData.usageSummary.geoBreakdown.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <GlobeIcon class="w-4 h-4 text-tokyo-night-cyan" />
                <h3>Top Regions</h3>
              </div>
              <span class="text-xs text-muted-foreground">
                {billingData.usageSummary.uniqueCountries} countries, {billingData.usageSummary.uniqueCities} cities
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
                  {#each billingData.usageSummary.geoBreakdown as country (country.countryCode)}
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
