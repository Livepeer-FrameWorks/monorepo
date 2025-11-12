<script>
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { infrastructureService } from "$lib/graphql/services/infrastructure.js";
  import { geographicService } from "$lib/graphql/services/geographic.js";
  import { routingService } from "$lib/graphql/services/routing.js";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";

  let isAuthenticated = false;
  let loading = $state(true);
  let nodes = $state([]);
  let geographicDistribution = $state(null);
  let loadBalancingMetrics = $state([]);
  let routingEfficiency = $state({ efficiency: 0, avgScore: 0, totalDecisions: 0 });
  let connectionPatterns = $state({ uniqueCountries: 0, mostPopularNodes: [], avgDistance: 0 });
  let error = $state(null);

  // Time range for analytics (last 24 hours)
  const timeRange = {
    start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    end: new Date().toISOString()
  };

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadAllData();
    loading = false;
  });

  async function loadAllData() {
    await Promise.all([
      loadNodeData(),
      loadGeographicData(),
      loadRoutingData()
    ]);
  }

  async function loadNodeData() {
    try {
      nodes = await infrastructureService.getNodes();
    } catch (err) {
      error = err.message;
      console.error("Failed to load node data:", err);
    }
  }

  async function loadGeographicData() {
    try {
      const [geoDist, loadBalancing] = await Promise.all([
        geographicService.getGeographicDistribution(null, timeRange),
        geographicService.getLoadBalancingMetrics(timeRange)
      ]);

      geographicDistribution = geoDist;
      loadBalancingMetrics = loadBalancing;
    } catch (err) {
      console.error("Failed to load geographic analytics:", err);
    }
  }

  async function loadRoutingData() {
    try {
      const [efficiency, , patterns] = await Promise.all([
        routingService.getRoutingEfficiency(null, timeRange),
        routingService.getRoutingEvents(null, timeRange),
        routingService.getConnectionPatterns(null, timeRange)
      ]);
      
      routingEfficiency = efficiency;
      connectionPatterns = patterns;
    } catch (err) {
      console.error("Failed to load routing analytics:", err);
    }
  }


  const SvelteComponent = $derived(getIconComponent('Globe'));
</script>

<svelte:head>
  <title>Geographic Analytics - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2 flex items-center">
        <SvelteComponent class="w-8 h-8 mr-3 text-tokyo-night-green" />
        Geographic Analytics
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Viewer distribution, infrastructure nodes, and geographic load balancing metrics
      </p>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center justify-center min-h-64">
      <div class="loading-spinner w-8 h-8"></div>
    </div>
  {:else if error}
    {@const SvelteComponent_1 = getIconComponent('AlertCircle')}
    <div class="card border-tokyo-night-red/30">
      <div class="text-center py-12">
        <div class="text-6xl mb-4">
          <SvelteComponent_1 class="w-16 h-16 text-tokyo-night-red mx-auto" />
        </div>
        <h3 class="text-xl font-semibold text-tokyo-night-red mb-2">
          Failed to Load Node Data
        </h3>
        <p class="text-tokyo-night-fg-dark mb-6">{error}</p>
        <Button onclick={loadAllData}>
          Try Again
        </Button>
      </div>
    </div>
  {:else}
    <!-- Geographic Distribution Overview -->
    {#if geographicDistribution}
      <div class="stats-panel">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
          Geographic Distribution (Last 24 Hours)
        </h2>
        
        <div class="grid md:grid-cols-4 gap-4 mb-6">
          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
            <div class="text-2xl font-bold text-tokyo-night-green mb-2">{geographicDistribution.totalViewers}</div>
            <div class="text-sm text-tokyo-night-comment">Total Viewers</div>
          </div>
          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
            <div class="text-2xl font-bold text-tokyo-night-blue mb-2">{geographicDistribution.uniqueCountries}</div>
            <div class="text-sm text-tokyo-night-comment">Countries</div>
          </div>
          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
            <div class="text-2xl font-bold text-tokyo-night-purple mb-2">{geographicDistribution.uniqueCities}</div>
            <div class="text-sm text-tokyo-night-comment">Cities</div>
          </div>
          <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
            <div class="text-2xl font-bold text-tokyo-night-orange mb-2">{loadBalancingMetrics.length}</div>
            <div class="text-sm text-tokyo-night-comment">Load Balancing Events</div>
          </div>
        </div>

        {#if geographicDistribution.topCountries?.length > 0}
          <div class="grid md:grid-cols-2 gap-6">
            <!-- Top Countries -->
            <div>
              <h3 class="text-lg font-medium text-tokyo-night-fg mb-4">Top Countries</h3>
              <div class="space-y-2">
                {#each geographicDistribution.topCountries.slice(0, 5) as country (country.countryCode)}
                  <div class="flex justify-between items-center p-3 bg-tokyo-night-bg-highlight rounded border border-tokyo-night-fg-gutter">
                    <span class="font-mono text-sm">{country.countryCode}</span>
                    <div class="text-right">
                      <div class="font-semibold">{country.viewerCount}</div>
                      <div class="text-xs text-tokyo-night-comment">{country.percentage.toFixed(1)}%</div>
                    </div>
                  </div>
                {/each}
              </div>
            </div>

            <!-- Top Cities -->
            <div>
              <h3 class="text-lg font-medium text-tokyo-night-fg mb-4">Top Cities</h3>
              <div class="space-y-2">
                {#each geographicDistribution.topCities.slice(0, 5) as city (`${city.city}-${city.countryCode}`)}
                  <div class="flex justify-between items-center p-3 bg-tokyo-night-bg-highlight rounded border border-tokyo-night-fg-gutter">
                    <div>
                      <div class="font-medium">{city.city}</div>
                      <div class="text-xs text-tokyo-night-comment font-mono">{city.countryCode}</div>
                    </div>
                    <div class="text-right">
                      <div class="font-semibold">{city.viewerCount}</div>
                      <div class="text-xs text-tokyo-night-comment">{city.percentage.toFixed(1)}%</div>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Routing Efficiency Metrics -->
    <div class="stats-panel">
      <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
        Routing Efficiency & Performance
      </h2>
      
      <div class="grid md:grid-cols-4 gap-4 mb-6">
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
          <div class="text-2xl font-bold text-tokyo-night-green mb-2">
            {routingEfficiency.efficiency?.toFixed(1) || 0}%
          </div>
          <div class="text-sm text-tokyo-night-comment">Routing Success</div>
        </div>
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
          <div class="text-2xl font-bold text-tokyo-night-blue mb-2">
            {routingEfficiency.avgScore?.toFixed(1) || 0}
          </div>
          <div class="text-sm text-tokyo-night-comment">Avg Route Score</div>
        </div>
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
          <div class="text-2xl font-bold text-tokyo-night-purple mb-2">
            {routingEfficiency.totalDecisions || 0}
          </div>
          <div class="text-sm text-tokyo-night-comment">Routing Decisions</div>
        </div>
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
          <div class="text-2xl font-bold text-tokyo-night-orange mb-2">
            {connectionPatterns.avgDistance?.toFixed(0) || 0}km
          </div>
          <div class="text-sm text-tokyo-night-comment">Avg Distance</div>
        </div>
      </div>

      <!-- Connection Patterns -->
      {#if connectionPatterns.mostPopularNodes?.length > 0}
        {@const SvelteComponent_2 = getIconComponent('Server')}
        {@const SvelteComponent_3 = getIconComponent('MapPin')}
        <div class="grid md:grid-cols-2 gap-6">
          <!-- Most Popular Nodes -->
          <div>
            <h3 class="text-lg font-medium text-tokyo-night-fg mb-4">
              <SvelteComponent_2 class="w-5 h-5 inline mr-2" />
              Most Popular Routing Targets
            </h3>
            <div class="space-y-2">
              {#each connectionPatterns.mostPopularNodes.slice(0, 5) as node (node.nodeId)}
                <div class="flex justify-between items-center p-3 bg-tokyo-night-bg-highlight rounded border border-tokyo-night-fg-gutter">
                  <span class="font-mono text-sm">{node.nodeName}</span>
                  <div class="text-right">
                    <div class="font-semibold">{node.connectionCount}</div>
                    <div class="text-xs text-tokyo-night-comment">
                      {node.successRate ? `${node.successRate.toFixed(1)}% success` : 'N/A'}
                    </div>
                  </div>
                </div>
              {/each}
            </div>
          </div>

          <!-- Connection Quality by Distance -->
          <div>
            <h3 class="text-lg font-medium text-tokyo-night-fg mb-4">
              <SvelteComponent_3 class="w-5 h-5 inline mr-2" />
              Connection Quality Distribution
            </h3>
            <div class="space-y-3">
              <div class="flex justify-between items-center">
                <span class="text-tokyo-night-comment">Short Range (&lt;500km)</span>
                <div class="flex items-center space-x-2">
                  <div class="w-32 bg-tokyo-night-bg-highlight rounded-full h-2">
                    <div class="bg-green-500 h-2 rounded-full" style="width: 85%"></div>
                  </div>
                  <span class="text-sm text-green-400">85%</span>
                </div>
              </div>
              <div class="flex justify-between items-center">
                <span class="text-tokyo-night-comment">Medium Range (500-2000km)</span>
                <div class="flex items-center space-x-2">
                  <div class="w-32 bg-tokyo-night-bg-highlight rounded-full h-2">
                    <div class="bg-yellow-500 h-2 rounded-full" style="width: 72%"></div>
                  </div>
                  <span class="text-sm text-yellow-400">72%</span>
                </div>
              </div>
              <div class="flex justify-between items-center">
                <span class="text-tokyo-night-comment">Long Range (&gt;2000km)</span>
                <div class="flex items-center space-x-2">
                  <div class="w-32 bg-tokyo-night-bg-highlight rounded-full h-2">
                    <div class="bg-orange-500 h-2 rounded-full" style="width: 58%"></div>
                  </div>
                  <span class="text-sm text-orange-400">58%</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      {/if}
    </div>

    <!-- Load Balancing Geographic Metrics -->
    {#if loadBalancingMetrics.length > 0}
      <div class="stats-panel">
        <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
          Load Balancing Geographic Data
        </h2>
        
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-tokyo-night-fg-gutter">
                <th class="text-left py-2 text-tokyo-night-comment">Stream</th>
                <th class="text-left py-2 text-tokyo-night-comment">Client Location</th>
                <th class="text-left py-2 text-tokyo-night-comment">Selected Node</th>
                <th class="text-left py-2 text-tokyo-night-comment">Distance (km)</th>
                <th class="text-left py-2 text-tokyo-night-comment">Status</th>
                <th class="text-left py-2 text-tokyo-night-comment">Time</th>
              </tr>
            </thead>
            <tbody>
              {#each loadBalancingMetrics.slice(0, 10) as metric (metric.id ?? `${metric.region}-${metric.timestamp}`)}
                <tr class="border-b border-tokyo-night-fg-gutter/30">
                  <td class="py-2 font-mono text-xs">{metric.stream}</td>
                  <td class="py-2">
                    {#if metric.clientCountry}
                      <div class="font-mono text-xs">{metric.clientCountry}</div>
                      {#if metric.clientLatitude && metric.clientLongitude}
                        <div class="text-xs text-tokyo-night-comment">
                          {metric.clientLatitude.toFixed(2)}, {metric.clientLongitude.toFixed(2)}
                        </div>
                      {/if}
                    {:else}
                      <span class="text-tokyo-night-comment">Unknown</span>
                    {/if}
                  </td>
                  <td class="py-2">
                    <div class="font-mono text-xs">{metric.selectedNode}</div>
                    {#if metric.nodeLatitude && metric.nodeLongitude}
                      <div class="text-xs text-tokyo-night-comment">
                        {metric.nodeLatitude.toFixed(2)}, {metric.nodeLongitude.toFixed(2)}
                      </div>
                    {/if}
                  </td>
                  <td class="py-2">
                    {#if metric.routingDistance}
                      <span class="font-semibold">{metric.routingDistance.toFixed(0)}</span>
                    {:else}
                      <span class="text-tokyo-night-comment">N/A</span>
                    {/if}
                  </td>
                  <td class="py-2">
                    <span class="px-2 py-1 rounded-full text-xs {metric.status === 'success' ? 'bg-green-500/20 text-green-500' : 'bg-red-500/20 text-red-500'}">
                      {metric.status}
                    </span>
                  </td>
                  <td class="py-2 text-xs text-tokyo-night-comment">
                    {new Date(metric.timestamp).toLocaleString()}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {/if}

    <!-- Regional Node Distribution -->
    <div class="stats-panel">
      <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
        Node Distribution by Region
      </h2>
      
      {#if nodes.length > 0}
        <div class="grid md:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each nodes as node (node.id ?? node.nodeId)}
            <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter">
              <div class="flex items-center justify-between mb-3">
                <h3 class="font-semibold text-tokyo-night-fg">{node.name}</h3>
                <span class="text-sm px-2 py-1 rounded-full {node.status === 'HEALTHY' ? 'bg-green-500/20 text-green-500' : 'bg-red-500/20 text-red-500'}">
                  {node.status}
                </span>
              </div>
              
              <div class="space-y-2 text-sm">
                <div class="flex justify-between">
                  <span class="text-tokyo-night-comment">Region:</span>
                  <span class="text-tokyo-night-fg">{node.region}</span>
                </div>
                <div class="flex justify-between">
                  <span class="text-tokyo-night-comment">Type:</span>
                  <span class="text-tokyo-night-fg">{node.type}</span>
                </div>
                {#if node.ipAddress}
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">IP:</span>
                    <span class="text-tokyo-night-fg font-mono text-xs">{node.ipAddress}</span>
                  </div>
                {/if}
                {#if node.latitude && node.longitude}
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Coordinates:</span>
                    <span class="text-tokyo-night-fg font-mono text-xs">{node.latitude.toFixed(2)}, {node.longitude.toFixed(2)}</span>
                  </div>
                {/if}
                {#if node.location}
                  <div class="flex justify-between">
                    <span class="text-tokyo-night-comment">Location:</span>
                    <span class="text-tokyo-night-fg text-xs">{node.location}</span>
                  </div>
                {/if}
                <div class="flex justify-between">
                  <span class="text-tokyo-night-comment">Last Seen:</span>
                  <span class="text-tokyo-night-fg text-xs">{new Date(node.lastSeen).toLocaleString()}</span>
                </div>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        {@const SvelteComponent_4 = getIconComponent('Monitor')}
        <div class="text-center py-12">
          <div class="text-6xl mb-4">
            <SvelteComponent_4 class="w-16 h-16 text-tokyo-night-fg mx-auto" />
          </div>
          <h3 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            No Infrastructure Nodes
          </h3>
          <p class="text-tokyo-night-fg-dark mb-6">
            Configure infrastructure nodes to see regional distribution
          </p>
          <Button href={resolve("/infrastructure")}>
            Configure Infrastructure
          </Button>
        </div>
      {/if}
    </div>
  {/if}
</div> 
