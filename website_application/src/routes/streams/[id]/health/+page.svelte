<script>
  import { onMount, onDestroy } from "svelte";
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { base } from "$app/paths";
  import { healthService } from "$lib/graphql/services/health.js";
  import { streamsService } from "$lib/graphql/services/streams.js";
  import HealthScoreIndicator from "$lib/components/health/HealthScoreIndicator.svelte";
  import BufferStateIndicator from "$lib/components/health/BufferStateIndicator.svelte";
  import QualityMetrics from "$lib/components/health/QualityMetrics.svelte";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import { getIconComponent } from "$lib/iconUtils.js";

  let streamId = $page.params.id;
  let stream = null;
  let currentHealth = null;
  let healthMetrics = [];
  let healthAlerts = [];
  let qualityChanges = [];
  let rebufferingEvents = [];
  let comprehensiveAnalysis = null;
  let loading = true;
  let error = null;

  // Time range for historical data (last 24 hours)
  const timeRange = {
    start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    end: new Date().toISOString()
  };

  // Auto-refresh interval
  let refreshInterval = null;

  onMount(async () => {
    await loadStreamData();
    await loadHealthData();
    await loadComprehensiveAnalysis();
    
    // Set up auto-refresh every 30 seconds for current health
    refreshInterval = setInterval(async () => {
      try {
        currentHealth = await healthService.getCurrentStreamHealth(streamId);
      } catch (err) {
        console.error('Failed to refresh health data:', err);
      }
    }, 30000);
  });

  onDestroy(() => {
    if (refreshInterval) {
      clearInterval(refreshInterval);
    }
  });

  async function loadStreamData() {
    try {
      const streams = await streamsService.getStreams();
      stream = streams.find(s => s.id === streamId);
      
      if (!stream) {
        error = "Stream not found";
        loading = false;
        return;
      }
    } catch (err) {
      console.error('Failed to load stream:', err);
      error = "Failed to load stream data";
      loading = false;
    }
  }

  async function loadHealthData() {
    try {
      loading = true;
      
      // Load all health data in parallel
      const [healthData, alertsData, changesData, rebufferData, currentData] = await Promise.all([
        healthService.getStreamHealthMetrics(streamId, timeRange),
        healthService.getStreamHealthAlerts(streamId, timeRange),
        healthService.getStreamQualityChanges(streamId, timeRange),
        healthService.getRebufferingEvents(streamId, timeRange),
        healthService.getCurrentStreamHealth(streamId)
      ]);

      healthMetrics = healthData || [];
      healthAlerts = alertsData || [];
      qualityChanges = changesData || [];
      rebufferingEvents = rebufferData || [];
      currentHealth = currentData;

    } catch (err) {
      console.error('Failed to load health data:', err);
      error = "Failed to load health monitoring data";
    } finally {
      loading = false;
    }
  }

  async function loadComprehensiveAnalysis() {
    try {
      comprehensiveAnalysis = await healthService.getComprehensiveHealthAnalysis(streamId, timeRange);
    } catch (err) {
      console.error('Failed to load comprehensive analysis:', err);
    }
  }

  function formatTimestamp(timestamp) {
    return new Date(timestamp).toLocaleString();
  }

  function getAlertTypeIcon(alertType) {
    switch (alertType) {
      case 'HIGH_JITTER': return 'Zap';
      case 'KEYFRAME_INSTABILITY': return 'Film';
      case 'PACKET_LOSS': return 'Wifi';
      case 'REBUFFERING': return 'Pause';
      case 'QUALITY_DEGRADATION': return 'TrendingDown';
      default: return 'AlertTriangle';
    }
  }

  function navigateBack() {
    goto(`${base}/streams`);
  }

  function getTrendColor(trend) {
    switch (trend) {
      case 'improving': return 'text-green-400';
      case 'degrading': return 'text-red-400';
      default: return 'text-tokyo-night-fg';
    }
  }

  function getTrendIcon(trend) {
    switch (trend) {
      case 'improving': return 'TrendingUp';
      case 'degrading': return 'TrendingDown';
      default: return 'Minus';
    }
  }

  function getStabilityColor(stability) {
    switch (stability) {
      case 'stable': return 'text-green-400';
      case 'minor-changes': return 'text-yellow-400';
      case 'unstable': return 'text-red-400';
      default: return 'text-tokyo-night-fg';
    }
  }

  function getImpactColor(impact) {
    switch (impact) {
      case 'none': return 'text-green-400';
      case 'low': return 'text-yellow-400';
      case 'medium': return 'text-orange-400';
      case 'high': return 'text-red-400';
      default: return 'text-tokyo-night-fg';
    }
  }
</script>

<svelte:head>
  <title>Stream Health - {stream?.name || 'Loading...'} - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Header -->
    <div class="mb-8">
      <div class="flex items-center space-x-4 mb-4">
        <button
          on:click={navigateBack}
          class="p-2 rounded-lg bg-tokyo-night-surface hover:bg-tokyo-night-selection transition-colors"
        >
          <svelte:component this={getIconComponent('ArrowLeft')} class="w-5 h-5" />
        </button>
        
        <div>
          <h1 class="text-3xl font-bold text-tokyo-night-blue">
            Stream Health Monitoring
          </h1>
          {#if stream}
            <p class="text-tokyo-night-comment">
              {stream.name} • Last 24 hours
            </p>
          {/if}
        </div>
      </div>
    </div>

    {#if loading}
      <div class="space-y-6">
        <LoadingCard variant="analytics" />
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <LoadingCard variant="analytics" />
          <LoadingCard variant="analytics" />
        </div>
      </div>
    {:else if error}
      <div class="bg-red-900/20 border border-red-500/30 rounded-lg p-6 text-center">
        <svelte:component this={getIconComponent('AlertTriangle')} class="w-12 h-12 text-red-400 mx-auto mb-4" />
        <h3 class="text-lg font-semibold text-red-400 mb-2">Error Loading Health Data</h3>
        <p class="text-red-300">{error}</p>
        <button
          on:click={loadHealthData}
          class="mt-4 px-4 py-2 bg-red-600 hover:bg-red-700 text-white rounded-lg transition-colors"
        >
          Retry
        </button>
      </div>
    {:else}
      <!-- Current Health Status -->
      {#if currentHealth}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold text-tokyo-night-cyan mb-6">Current Health Status</h2>
          
          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <!-- Health Score -->
            <div class="flex items-center justify-center">
              <HealthScoreIndicator 
                healthScore={currentHealth.healthScore} 
                size="lg" 
              />
            </div>

            <!-- Buffer State -->
            <div class="flex items-center justify-center">
              <BufferStateIndicator 
                bufferState={currentHealth.bufferState}
                bufferHealth={currentHealth.bufferHealth}
                size="lg" 
              />
            </div>

            <!-- Key Metrics -->
            <div class="space-y-3">
              <div class="flex justify-between">
                <span class="text-tokyo-night-fg">Quality Tier:</span>
                <span class="font-mono text-tokyo-night-cyan">{currentHealth.qualityTier || 'Unknown'}</span>
              </div>
              <div class="flex justify-between">
                <span class="text-tokyo-night-fg">Frame Jitter:</span>
                <span class="font-mono {currentHealth.frameJitterMs > 30 ? 'text-red-400' : 'text-green-400'}">
                  {currentHealth.frameJitterMs ? `${currentHealth.frameJitterMs.toFixed(1)}ms` : 'N/A'}
                </span>
              </div>
              <div class="flex justify-between">
                <span class="text-tokyo-night-fg">Packet Loss:</span>
                <span class="font-mono {currentHealth.packetLossPercentage > 2 ? 'text-red-400' : 'text-green-400'}">
                  {currentHealth.packetLossPercentage ? `${currentHealth.packetLossPercentage.toFixed(2)}%` : 'N/A'}
                </span>
              </div>
              {#if currentHealth.issuesDescription}
                <div class="mt-4">
                  <span class="text-tokyo-night-fg">Issues:</span>
                  <p class="text-sm text-red-400 mt-1">{currentHealth.issuesDescription}</p>
                </div>
              {/if}
            </div>
          </div>
        </div>
      {/if}

      <!-- Comprehensive Health Analysis -->
      {#if comprehensiveAnalysis?.analysis}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold text-tokyo-night-cyan mb-6">Health Analysis Summary</h2>
          
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6">
            <!-- Overall Health Score -->
            <div class="text-center">
              <div class="relative inline-flex items-center justify-center">
                <div class="w-20 h-20 rounded-full border-4 {comprehensiveAnalysis.analysis.overallScore >= 80 ? 'border-green-400' : comprehensiveAnalysis.analysis.overallScore >= 60 ? 'border-yellow-400' : 'border-red-400'} flex items-center justify-center">
                  <span class="text-2xl font-bold {comprehensiveAnalysis.analysis.overallScore >= 80 ? 'text-green-400' : comprehensiveAnalysis.analysis.overallScore >= 60 ? 'text-yellow-400' : 'text-red-400'}">
                    {comprehensiveAnalysis.analysis.overallScore}
                  </span>
                </div>
              </div>
              <p class="text-sm text-tokyo-night-comment mt-2">Overall Score</p>
            </div>

            <!-- Health Trend -->
            <div class="text-center">
              <div class="flex items-center justify-center space-x-2 mb-2">
                <svelte:component 
                  this={getIconComponent(getTrendIcon(comprehensiveAnalysis.analysis.healthTrend))} 
                  class="w-6 h-6 {getTrendColor(comprehensiveAnalysis.analysis.healthTrend)}" 
                />
                <span class={getTrendColor(comprehensiveAnalysis.analysis.healthTrend)}>
                  {comprehensiveAnalysis.analysis.healthTrend}
                </span>
              </div>
              <p class="text-sm text-tokyo-night-comment">Health Trend</p>
            </div>

            <!-- Quality Stability -->
            <div class="text-center">
              <div class="flex items-center justify-center space-x-2 mb-2">
                <svelte:component 
                  this={getIconComponent('Activity')} 
                  class="w-6 h-6 {getStabilityColor(comprehensiveAnalysis.analysis.qualityStability)}" 
                />
                <span class={getStabilityColor(comprehensiveAnalysis.analysis.qualityStability)}>
                  {comprehensiveAnalysis.analysis.qualityStability.replace('-', ' ')}
                </span>
              </div>
              <p class="text-sm text-tokyo-night-comment">Quality Stability</p>
            </div>

            <!-- Rebuffer Impact -->
            <div class="text-center">
              <div class="flex items-center justify-center space-x-2 mb-2">
                <svelte:component 
                  this={getIconComponent('Pause')} 
                  class="w-6 h-6 {getImpactColor(comprehensiveAnalysis.analysis.rebufferImpact)}" 
                />
                <span class={getImpactColor(comprehensiveAnalysis.analysis.rebufferImpact)}>
                  {comprehensiveAnalysis.analysis.rebufferImpact} impact
                </span>
              </div>
              <p class="text-sm text-tokyo-night-comment">Rebuffer Impact</p>
            </div>
          </div>
        </div>
      {/if}

      <div class="grid grid-cols-1 lg:grid-cols-2 gap-8">
        <!-- Health Alerts -->
        <div class="bg-tokyo-night-surface rounded-lg p-6">
          <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Recent Health Alerts</h3>
          
          {#if healthAlerts.length > 0}
            <div class="space-y-3 max-h-96 overflow-y-auto">
              {#each healthAlerts.slice(0, 10) as alert}
                <div class="border border-tokyo-night-selection rounded-lg p-3">
                  <div class="flex items-start space-x-3">
                    <svelte:component 
                      this={getIconComponent(getAlertTypeIcon(alert.alertType))} 
                      class="w-5 h-5 {healthService.getAlertSeverityColor(alert.severity)} mt-0.5" 
                    />
                    <div class="flex-1">
                      <div class="flex justify-between items-start">
                        <h4 class="font-medium text-tokyo-night-fg">{alert.alertType.replace('_', ' ')}</h4>
                        <span class="text-xs text-tokyo-night-comment">{formatTimestamp(alert.timestamp)}</span>
                      </div>
                      <p class="text-sm text-tokyo-night-comment mt-1">
                        Severity: <span class={healthService.getAlertSeverityColor(alert.severity)}>{alert.severity}</span>
                      </p>
                      {#if alert.issuesDescription}
                        <p class="text-sm text-tokyo-night-fg mt-1">{alert.issuesDescription}</p>
                      {/if}
                    </div>
                  </div>
                </div>
              {/each}
            </div>
          {:else}
            <p class="text-tokyo-night-comment text-center py-8">No health alerts in the last 24 hours</p>
          {/if}
        </div>

        <!-- Quality Changes -->
        <div class="bg-tokyo-night-surface rounded-lg p-6">
          <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Quality Changes</h3>
          
          {#if qualityChanges.length > 0}
            <div class="space-y-3 max-h-96 overflow-y-auto">
              {#each qualityChanges.slice(0, 10) as change}
                <div class="border border-tokyo-night-selection rounded-lg p-3">
                  <div class="flex justify-between items-start mb-2">
                    <h4 class="font-medium text-tokyo-night-fg">{change.changeType.replace('_', ' ')}</h4>
                    <span class="text-xs text-tokyo-night-comment">{formatTimestamp(change.timestamp)}</span>
                  </div>
                  
                  {#if change.previousQualityTier && change.newQualityTier}
                    <p class="text-sm text-tokyo-night-fg">
                      Quality: <span class="text-red-400">{change.previousQualityTier}</span> → 
                      <span class="text-green-400">{change.newQualityTier}</span>
                    </p>
                  {/if}
                  
                  {#if change.previousResolution && change.newResolution}
                    <p class="text-sm text-tokyo-night-fg">
                      Resolution: <span class="text-red-400">{change.previousResolution}</span> → 
                      <span class="text-green-400">{change.newResolution}</span>
                    </p>
                  {/if}
                  
                  {#if change.previousCodec && change.newCodec}
                    <p class="text-sm text-tokyo-night-fg">
                      Codec: <span class="text-red-400">{change.previousCodec}</span> → 
                      <span class="text-green-400">{change.newCodec}</span>
                    </p>
                  {/if}
                </div>
              {/each}
            </div>
          {:else}
            <p class="text-tokyo-night-comment text-center py-8">No quality changes detected</p>
          {/if}
        </div>
      </div>

      <!-- Rebuffering Events -->
      {#if rebufferingEvents.length > 0}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mt-8">
          <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Rebuffering Events</h3>
          
          <div class="space-y-3 max-h-64 overflow-y-auto">
            {#each rebufferingEvents.slice(0, 10) as event}
              <div class="border border-tokyo-night-selection rounded-lg p-3">
                <div class="flex justify-between items-start">
                  <div class="flex items-center space-x-2">
                    <svelte:component this={getIconComponent('Pause')} class="w-4 h-4 text-orange-400" />
                    <span class="font-medium text-tokyo-night-fg">
                      {event.rebufferStart ? 'Rebuffer Started' : 'Rebuffer Ended'}
                    </span>
                  </div>
                  <span class="text-xs text-tokyo-night-comment">{formatTimestamp(event.timestamp)}</span>
                </div>
                
                <div class="mt-2 grid grid-cols-3 gap-4 text-sm">
                  <div>
                    <span class="text-tokyo-night-comment">Buffer State:</span>
                    <span class={healthService.getBufferStateColor(event.bufferState)}>{event.bufferState}</span>
                  </div>
                  <div>
                    <span class="text-tokyo-night-comment">Health Score:</span>
                    <span class={healthService.getHealthScoreColor(event.healthScore)}>
                      {Math.round(event.healthScore * 100)}%
                    </span>
                  </div>
                  <div>
                    <span class="text-tokyo-night-comment">Packet Loss:</span>
                    <span class={event.packetLossPercentage > 2 ? 'text-red-400' : 'text-green-400'}>
                      {event.packetLossPercentage ? `${event.packetLossPercentage.toFixed(2)}%` : 'N/A'}
                    </span>
                  </div>
                </div>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Historical Health Metrics Chart Placeholder -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mt-8">
        <h3 class="text-lg font-semibold text-tokyo-night-cyan mb-4">Historical Health Trends</h3>
        
        {#if healthMetrics.length > 0}
          <div class="text-center py-8">
            <svelte:component this={getIconComponent('TrendingUp')} class="w-12 h-12 text-tokyo-night-comment mx-auto mb-4" />
            <p class="text-tokyo-night-comment">
              Chart visualization coming soon<br>
              <span class="text-sm">Collected {healthMetrics.length} data points over the last 24 hours</span>
            </p>
          </div>
        {:else}
          <p class="text-tokyo-night-comment text-center py-8">No historical health data available</p>
        {/if}
      </div>
    {/if}
  </div>
</div>