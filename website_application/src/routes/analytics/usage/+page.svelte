<script>
  import { onMount } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { analyticsAPIFunctions, billingAPI } from "$lib/api";

  let isAuthenticated = false;
  let user = null;
  let loading = true;
  let error = null;

  // Usage data from Periscope
  let usageData = {
    stream_hours: 0,
    egress_gb: 0,
    recording_gb: 0,
    peak_bandwidth_mbps: 0,
    total_streams: 0,
    total_viewers: 0,
    peak_viewers: 0,
    period: ''
  };

  // Billing data from Purser
  let billingData = {
    tier: null,
    subscription: null
  };

  // Time range for usage query
  let timeRange = '7d'; // 7d, 30d, 90d
  let startTime = '';
  let endTime = '';

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    user = authState.user?.user || null;
  });

  onMount(async () => {
    await loadUsageAndCosts();
  });

  async function loadUsageAndCosts() {
    loading = true;
    error = null;

    try {
      // Set time range based on selection
      const now = new Date();
      const days = timeRange === '7d' ? 7 : timeRange === '30d' ? 30 : 90;
      startTime = new Date(now.getTime() - days * 24 * 60 * 60 * 1000).toISOString();
      endTime = now.toISOString();

      // Load usage data from Periscope and billing data from Purser in parallel
      const [usageResponse, billingResponse] = await Promise.all([
        analyticsAPIFunctions.getUsageSummary({ start_time: startTime, end_time: endTime }),
        billingAPI.getBillingStatus()
      ]);

      // Process usage data
      if (usageResponse.data) {
        usageData = {
          ...usageData,
          ...usageResponse.data,
          period: `${days} days`
        };
      }

      // Process billing data
      if (billingResponse.data) {
        billingData = billingResponse.data;
      }

      console.log('Usage data loaded:', usageData);
      console.log('Billing data loaded:', billingData);

    } catch (err) {
      error = err?.response?.data?.error || err?.message || 'Failed to load usage and costs data';
      console.error('Failed to load usage and costs:', err);
    } finally {
      loading = false;
    }
  }

  // Calculate estimated costs based on usage and tier pricing
  function calculateEstimatedCosts() {
    if (!billingData.tier || !billingData.tier.base_price) {
      return { total: 0, breakdown: {} };
    }

    // Basic cost calculation (this would be more sophisticated in production)
    const baseCost = billingData.tier.base_price || 0;
    const bandwidthCost = usageData.egress_gb * 0.05; // Example: $0.05 per GB
    const streamingCost = usageData.stream_hours * 0.10; // Example: $0.10 per hour
    const storageCost = usageData.recording_gb * 0.02; // Example: $0.02 per GB stored

    return {
      total: baseCost + bandwidthCost + streamingCost + storageCost,
      breakdown: {
        base: baseCost,
        bandwidth: bandwidthCost,
        streaming: streamingCost,
        storage: storageCost
      }
    };
  }

  $: estimatedCosts = calculateEstimatedCosts();

  // Format bytes to human readable
  function formatBytes(bytes, decimals = 2) {
    if (bytes === 0) return '0 Bytes';
    const k = 1024;
    const dm = decimals < 0 ? 0 : decimals;
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
  }

  // Format currency
  function formatCurrency(amount, currency = 'USD') {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency: currency
    }).format(amount);
  }

  // Format number with commas
  function formatNumber(num) {
    return new Intl.NumberFormat().format(Math.round(num));
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
      <select bind:value={timeRange} on:change={loadUsageAndCosts} class="input">
        <option value="7d">Last 7 days</option>
        <option value="30d">Last 30 days</option>
        <option value="90d">Last 90 days</option>
      </select>
      
      <button on:click={loadUsageAndCosts} class="btn-secondary">
        <span class="mr-2">🔄</span>
        Refresh
      </button>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center justify-center min-h-64">
      <div class="text-center">
        <div class="animate-spin w-8 h-8 border-2 border-tokyo-night-blue border-t-transparent rounded-full mx-auto mb-4"></div>
        <p class="text-tokyo-night-comment">Loading usage and costs data...</p>
      </div>
    </div>
  {:else if error}
    <div class="bg-tokyo-night-red/10 border border-tokyo-night-red/30 rounded-lg p-4">
      <div class="flex items-center space-x-2">
        <span class="text-tokyo-night-red">❌</span>
        <span class="text-tokyo-night-red font-medium">Error loading data</span>
      </div>
      <p class="text-tokyo-night-red/80 text-sm mt-1">{error}</p>
    </div>
  {:else}
    <!-- Current Plan & Billing Status -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <!-- Plan Information -->
      <div class="bg-tokyo-night-bg-highlight p-6 rounded-lg border border-tokyo-night-fg-gutter">
        <h2 class="text-lg font-semibold text-tokyo-night-fg mb-4">Your Plan</h2>
        
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <span class="text-tokyo-night-comment">Plan</span>
            <span class="text-tokyo-night-fg font-semibold">
              {billingData.tier?.display_name || 'Free'}
            </span>
          </div>
          
          <div class="flex items-center justify-between">
            <span class="text-tokyo-night-comment">Monthly Cost</span>
            <span class="text-tokyo-night-green font-semibold">
              {billingData.tier?.base_price ? formatCurrency(billingData.tier.base_price) : 'Free'}
            </span>
          </div>
          
          <div class="flex items-center justify-between">
            <span class="text-tokyo-night-comment">Status</span>
            <span class="bg-tokyo-night-green/20 text-tokyo-night-green px-2 py-1 rounded text-sm capitalize">
              {billingData.subscription?.status || 'active'}
            </span>
          </div>
        </div>
      </div>

      <!-- Estimated Costs -->
      <div class="bg-tokyo-night-bg-highlight p-6 rounded-lg border border-tokyo-night-fg-gutter">
        <h2 class="text-lg font-semibold text-tokyo-night-fg mb-4">What This Period Cost You</h2>
        
        <div class="space-y-3">
          {#if estimatedCosts.breakdown.base > 0}
            <div class="flex items-center justify-between text-sm">
              <span class="text-tokyo-night-comment">Base plan</span>
              <span class="text-tokyo-night-fg">{formatCurrency(estimatedCosts.breakdown.base)}</span>
            </div>
          {/if}
          
          {#if estimatedCosts.breakdown.streaming > 0}
            <div class="flex items-center justify-between text-sm">
              <span class="text-tokyo-night-comment">Streaming ({formatNumber(usageData.stream_hours)}h)</span>
              <span class="text-tokyo-night-fg">{formatCurrency(estimatedCosts.breakdown.streaming)}</span>
            </div>
          {/if}
          
          {#if estimatedCosts.breakdown.bandwidth > 0}
            <div class="flex items-center justify-between text-sm">
              <span class="text-tokyo-night-comment">Bandwidth ({formatNumber(usageData.egress_gb)} GB)</span>
              <span class="text-tokyo-night-fg">{formatCurrency(estimatedCosts.breakdown.bandwidth)}</span>
            </div>
          {/if}
          
          {#if estimatedCosts.breakdown.storage > 0}
            <div class="flex items-center justify-between text-sm">
              <span class="text-tokyo-night-comment">Storage ({formatNumber(usageData.recording_gb)} GB)</span>
              <span class="text-tokyo-night-fg">{formatCurrency(estimatedCosts.breakdown.storage)}</span>
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
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
      <!-- Stream Hours -->
      <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm text-tokyo-night-comment mb-1">Stream Hours</h3>
            <p class="text-2xl font-bold text-tokyo-night-blue">
              {formatNumber(usageData.stream_hours)}
            </p>
          </div>
          <span class="text-2xl">⏱️</span>
        </div>
      </div>

      <!-- Bandwidth Usage -->
      <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm text-tokyo-night-comment mb-1">Bandwidth Out</h3>
            <p class="text-2xl font-bold text-tokyo-night-green">
              {formatNumber(usageData.egress_gb)} GB
            </p>
          </div>
          <span class="text-2xl">📡</span>
        </div>
      </div>

      <!-- Peak Viewers -->
      <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm text-tokyo-night-comment mb-1">Peak Viewers</h3>
            <p class="text-2xl font-bold text-tokyo-night-purple">
              {formatNumber(usageData.peak_viewers)}
            </p>
          </div>
          <span class="text-2xl">👥</span>
        </div>
      </div>

      <!-- Total Streams -->
      <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm text-tokyo-night-comment mb-1">Total Streams</h3>
            <p class="text-2xl font-bold text-tokyo-night-cyan">
              {formatNumber(usageData.total_streams)}
            </p>
          </div>
          <span class="text-2xl">🎥</span>
        </div>
      </div>
    </div>

    <!-- Additional Metrics -->
    {#if usageData.recording_gb > 0 || usageData.peak_bandwidth_mbps > 0}
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        {#if usageData.recording_gb > 0}
          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
            <div class="flex items-center justify-between">
              <div>
                <h3 class="text-sm text-tokyo-night-comment mb-1">Recording Storage</h3>
                <p class="text-xl font-bold text-tokyo-night-yellow">
                  {formatNumber(usageData.recording_gb)} GB
                </p>
              </div>
              <span class="text-2xl">💾</span>
            </div>
          </div>
        {/if}

        {#if usageData.peak_bandwidth_mbps > 0}
          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
            <div class="flex items-center justify-between">
              <div>
                <h3 class="text-sm text-tokyo-night-comment mb-1">Peak Bandwidth</h3>
                <p class="text-xl font-bold text-tokyo-night-red">
                  {formatNumber(usageData.peak_bandwidth_mbps)} Mbps
                </p>
              </div>
              <span class="text-2xl">📈</span>
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Footer Note -->
    <div class="bg-tokyo-night-yellow/10 border border-tokyo-night-yellow/30 rounded-lg p-4">
      <div class="flex items-start space-x-3">
        <span class="text-tokyo-night-yellow mt-0.5">💡</span>
        <div>
          <h3 class="text-tokyo-night-yellow font-medium mb-1">How We Calculate This</h3>
          <p class="text-tokyo-night-yellow/80 text-sm">
            Your usage data comes from real streaming activity, and costs are estimated based on your current plan. 
            Don't worry - your actual bill will match your subscription terms, and we'll never surprise you with extra charges.
          </p>
        </div>
      </div>
    </div>
  {/if}
</div>