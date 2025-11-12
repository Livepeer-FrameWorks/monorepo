<script>
  import { onMount } from "svelte";
  import { billingService } from "$lib/graphql/services/billing.js";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import {
    Select,
    SelectTrigger,
    SelectContent,
    SelectItem,
  } from "$lib/components/ui/select";

  let loading = $state(true);
  let error = $state(null);

  // Usage data from Periscope
  let usageData = $state({
    stream_hours: 0,
    egress_gb: 0,
    recording_gb: 0,
    peak_bandwidth_mbps: 0,
    total_streams: 0,
    total_viewers: 0,
    peak_viewers: 0,
    period: "",
  });

  // Billing data from Purser
  let billingData = $state({
    tier: null,
    subscription: null,
  });

  // Time range for usage query
  let timeRange = $state("7d"); // 7d, 30d, 90d
  const timeRangeLabels = {
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
    loading = true;
    error = null;

    try {
      // Set time range based on selection
      const now = new Date();
      const days = timeRange === "7d" ? 7 : timeRange === "30d" ? 30 : 90;
      startTime = new Date(
        now.getTime() - days * 24 * 60 * 60 * 1000,
      ).toISOString();
      endTime = now.toISOString();

      // Load usage data and billing data from GraphQL
      const [usageRecords, billingStatus] = await Promise.all([
        billingService.getUsageRecords({
          start: new Date(startTime),
          end: new Date(endTime),
        }),
        billingService.getBillingStatus(),
      ]);

      // Process usage data - aggregate usage records
      if (usageRecords && usageRecords.length > 0) {
        const aggregated = usageRecords.reduce(
          (acc, record) => {
            switch (record.resourceType) {
              case "stream_hours":
                acc.stream_hours += record.quantity;
                break;
              case "egress_gb":
                acc.egress_gb += record.quantity;
                break;
              case "recording_gb":
                acc.recording_gb += record.quantity;
                break;
              case "peak_bandwidth_mbps":
                acc.peak_bandwidth_mbps = Math.max(
                  acc.peak_bandwidth_mbps,
                  record.quantity,
                );
                break;
              case "total_streams":
                acc.total_streams = Math.max(
                  acc.total_streams,
                  record.quantity,
                );
                break;
              case "peak_viewers":
                acc.peak_viewers = Math.max(acc.peak_viewers, record.quantity);
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

      // Process billing data
      billingData = billingStatus || {};
    } catch (err) {
      error =
        err?.response?.data?.error ||
        err?.message ||
        "Failed to load usage and costs data";
      console.error("Failed to load usage and costs:", err);
    } finally {
      loading = false;
    }
  }

  // Calculate estimated costs based on usage and tier pricing
  function calculateEstimatedCosts() {
    if (!billingData.currentTier || !billingData.currentTier.price) {
      return { total: 0, breakdown: {} };
    }

    // Basic cost calculation (this would be more sophisticated in production)
    const baseCost = billingData.currentTier.price || 0;
    const bandwidthCost = usageData.egress_gb * 0.05; // Example: $0.05 per GB
    const streamingCost = usageData.stream_hours * 0.1; // Example: $0.10 per hour
    const storageCost = usageData.recording_gb * 0.02; // Example: $0.02 per GB stored

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

  let estimatedCosts = $derived(calculateEstimatedCosts());
  // Format currency
  function formatCurrency(amount, currency = "USD") {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: currency,
    }).format(amount);
  }

  // Format number with commas
  function formatNumber(num) {
    return new Intl.NumberFormat().format(Math.round(num));
  }

  const SvelteComponent = $derived(getIconComponent("RefreshCw"));

  function handleTimeRangeChange(detail) {
    if (detail && detail !== timeRange) {
      timeRange = detail;
      loadUsageAndCosts();
    }
  }
</script>

<svelte:head>
  <title>Usage & Costs - FrameWorks</title>
</svelte:head>

<div class="space-y-6">
  <!-- Header -->
  <div class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-bold text-tokyo-night-fg">Your Usage & Costs</h1>
      <p class="text-tokyo-night-comment mt-1">
        Keep track of how much you're streaming and what it's costing you
      </p>
    </div>

    <div class="flex items-center space-x-3">
      <!-- Time Range Selector -->
      <Select
        bind:value={timeRange}
        on:valueChange={(event) => handleTimeRangeChange(event.detail)}
      >
        <SelectTrigger class="min-w-[160px]">
          {timeRangeLabels[timeRange] ?? "Last 7 days"}
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="7d">Last 7 days</SelectItem>
          <SelectItem value="30d">Last 30 days</SelectItem>
          <SelectItem value="90d">Last 90 days</SelectItem>
        </SelectContent>
      </Select>

      <Button variant="secondary" onclick={loadUsageAndCosts}>
        <SvelteComponent class="w-4 h-4 mr-2" />
        Refresh
      </Button>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center justify-center min-h-64">
      <div class="text-center">
        <div
          class="animate-spin w-8 h-8 border-2 border-tokyo-night-blue border-t-transparent rounded-full mx-auto mb-4"
        ></div>
        <p class="text-tokyo-night-comment">Loading usage and costs data...</p>
      </div>
    </div>
  {:else if error}
    {@const SvelteComponent_1 = getIconComponent("AlertCircle")}
    <div
      class="bg-tokyo-night-red/10 border border-tokyo-night-red/30 rounded-lg p-4"
    >
      <div class="flex items-center space-x-2">
        <SvelteComponent_1 class="w-5 h-5 text-tokyo-night-red" />
        <span class="text-tokyo-night-red font-medium">Error loading data</span>
      </div>
      <p class="text-tokyo-night-red/80 text-sm mt-1">{error}</p>
    </div>
  {:else}
    <!-- Current Plan & Billing Status -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <!-- Plan Information -->
      <div
        class="bg-tokyo-night-bg-highlight p-6 rounded-lg border border-tokyo-night-fg-gutter"
      >
        <h2 class="text-lg font-semibold text-tokyo-night-fg mb-4">
          Your Plan
        </h2>

        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <span class="text-tokyo-night-comment">Plan</span>
            <span class="text-tokyo-night-fg font-semibold">
              {billingData.currentTier?.name || "Free"}
            </span>
          </div>

          <div class="flex items-center justify-between">
            <span class="text-tokyo-night-comment">Monthly Cost</span>
            <span class="text-tokyo-night-green font-semibold">
              {billingData.currentTier?.price
                ? formatCurrency(
                    billingData.currentTier.price,
                    billingData.currentTier.currency,
                  )
                : "Free"}
            </span>
          </div>

          <div class="flex items-center justify-between">
            <span class="text-tokyo-night-comment">Status</span>
            <span
              class="bg-tokyo-night-green/20 text-tokyo-night-green px-2 py-1 rounded text-sm capitalize"
            >
              {billingData.status || "active"}
            </span>
          </div>
        </div>
      </div>

      <!-- Estimated Costs -->
      <div
        class="bg-tokyo-night-bg-highlight p-6 rounded-lg border border-tokyo-night-fg-gutter"
      >
        <h2 class="text-lg font-semibold text-tokyo-night-fg mb-4">
          What This Period Cost You
        </h2>

        <div class="space-y-3">
          {#if estimatedCosts.breakdown.base > 0}
            <div class="flex items-center justify-between text-sm">
              <span class="text-tokyo-night-comment">Base plan</span>
              <span class="text-tokyo-night-fg"
                >{formatCurrency(estimatedCosts.breakdown.base)}</span
              >
            </div>
          {/if}

          {#if estimatedCosts.breakdown.streaming > 0}
            <div class="flex items-center justify-between text-sm">
              <span class="text-tokyo-night-comment"
                >Streaming ({formatNumber(usageData.stream_hours)}h)</span
              >
              <span class="text-tokyo-night-fg"
                >{formatCurrency(estimatedCosts.breakdown.streaming)}</span
              >
            </div>
          {/if}

          {#if estimatedCosts.breakdown.bandwidth > 0}
            <div class="flex items-center justify-between text-sm">
              <span class="text-tokyo-night-comment"
                >Bandwidth ({formatNumber(usageData.egress_gb)} GB)</span
              >
              <span class="text-tokyo-night-fg"
                >{formatCurrency(estimatedCosts.breakdown.bandwidth)}</span
              >
            </div>
          {/if}

          {#if estimatedCosts.breakdown.storage > 0}
            <div class="flex items-center justify-between text-sm">
              <span class="text-tokyo-night-comment"
                >Storage ({formatNumber(usageData.recording_gb)} GB)</span
              >
              <span class="text-tokyo-night-fg"
                >{formatCurrency(estimatedCosts.breakdown.storage)}</span
              >
            </div>
          {/if}

          <hr class="border-tokyo-night-fg-gutter" />
          <div class="flex items-center justify-between">
            <span class="text-tokyo-night-fg font-medium">Estimated Total</span>
            <span class="text-tokyo-night-green font-bold text-lg">
              {formatCurrency(estimatedCosts.total)}
            </span>
          </div>
        </div>
      </div>
    </div>

    <!-- Usage Metrics -->
    {@const SvelteComponent_2 = getIconComponent("Clock")}
    {@const SvelteComponent_3 = getIconComponent("Radio")}
    {@const SvelteComponent_4 = getIconComponent("Users")}
    {@const SvelteComponent_5 = getIconComponent("Video")}
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
      <!-- Stream Hours -->
      <div
        class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
      >
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm text-tokyo-night-comment mb-1">Stream Hours</h3>
            <p class="text-2xl font-bold text-tokyo-night-blue">
              {formatNumber(usageData.stream_hours)}
            </p>
          </div>
          <SvelteComponent_2 class="w-6 h-6 text-tokyo-night-blue" />
        </div>
      </div>

      <!-- Bandwidth Usage -->
      <div
        class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
      >
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm text-tokyo-night-comment mb-1">Bandwidth Out</h3>
            <p class="text-2xl font-bold text-tokyo-night-green">
              {formatNumber(usageData.egress_gb)} GB
            </p>
          </div>
          <SvelteComponent_3 class="w-6 h-6 text-tokyo-night-green" />
        </div>
      </div>

      <!-- Peak Viewers -->
      <div
        class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
      >
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm text-tokyo-night-comment mb-1">Peak Viewers</h3>
            <p class="text-2xl font-bold text-tokyo-night-purple">
              {formatNumber(usageData.peak_viewers)}
            </p>
          </div>
          <SvelteComponent_4 class="w-6 h-6 text-tokyo-night-purple" />
        </div>
      </div>

      <!-- Total Streams -->
      <div
        class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
      >
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm text-tokyo-night-comment mb-1">Total Streams</h3>
            <p class="text-2xl font-bold text-tokyo-night-cyan">
              {formatNumber(usageData.total_streams)}
            </p>
          </div>
          <SvelteComponent_5 class="w-6 h-6 text-tokyo-night-cyan" />
        </div>
      </div>
    </div>

    <!-- Additional Metrics -->
    {#if usageData.recording_gb > 0 || usageData.peak_bandwidth_mbps > 0}
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        {#if usageData.recording_gb > 0}
          {@const SvelteComponent_6 = getIconComponent("HardDrive")}
          <div
            class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
          >
            <div class="flex items-center justify-between">
              <div>
                <h3 class="text-sm text-tokyo-night-comment mb-1">
                  Recording Storage
                </h3>
                <p class="text-xl font-bold text-tokyo-night-yellow">
                  {formatNumber(usageData.recording_gb)} GB
                </p>
              </div>
              <SvelteComponent_6 class="w-6 h-6 text-tokyo-night-yellow" />
            </div>
          </div>
        {/if}

        {#if usageData.peak_bandwidth_mbps > 0}
          {@const SvelteComponent_7 = getIconComponent("TrendingUp")}
          <div
            class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter"
          >
            <div class="flex items-center justify-between">
              <div>
                <h3 class="text-sm text-tokyo-night-comment mb-1">
                  Peak Bandwidth
                </h3>
                <p class="text-xl font-bold text-tokyo-night-red">
                  {formatNumber(usageData.peak_bandwidth_mbps)} Mbps
                </p>
              </div>
              <SvelteComponent_7 class="w-6 h-6 text-tokyo-night-red" />
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Footer Note -->
    {@const SvelteComponent_8 = getIconComponent("Lightbulb")}
    <div
      class="bg-tokyo-night-yellow/10 border border-tokyo-night-yellow/30 rounded-lg p-4"
    >
      <div class="flex items-start space-x-3">
        <SvelteComponent_8 class="w-5 h-5 text-tokyo-night-yellow mt-0.5" />
        <div>
          <h3 class="text-tokyo-night-yellow font-medium mb-1">
            How We Calculate This
          </h3>
          <p class="text-tokyo-night-yellow/80 text-sm">
            Your usage data comes from real streaming activity, and costs are
            estimated based on your current plan. Don't worry - your actual bill
            will match your subscription terms, and we'll never surprise you
            with extra charges.
          </p>
        </div>
      </div>
    </div>
  {/if}
</div>
