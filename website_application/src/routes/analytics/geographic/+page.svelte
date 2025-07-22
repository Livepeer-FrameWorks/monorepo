<script>
  import { onMount } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { analyticsAPIFunctions } from "$lib/api";

  let isAuthenticated = false;
  let loading = true;
  let geographicData = [];
  let selectedStream = null;
  let streams = [];
  let error = null;

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
    streams = authState.user?.streams || [];
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    if (streams.length > 0) {
      selectedStream = streams[0];
    }
    await loadGeographicData();
    loading = false;
  });

  async function loadGeographicData() {
    if (!selectedStream) return;
    
    try {
      // TODO: Create actual geographic analytics endpoint
      // const response = await analyticsAPIFunctions.getGeographicAnalytics(selectedStream.id);
      
      // Mock data based on what we collect from MistServer
      geographicData = [
        { country: "United States", viewers: 45, percentage: 45, latitude: 39.8283, longitude: -98.5795 },
        { country: "Germany", viewers: 23, percentage: 23, latitude: 51.1657, longitude: 10.4515 },
        { country: "United Kingdom", viewers: 18, percentage: 18, latitude: 55.3781, longitude: -3.4360 },
        { country: "Canada", viewers: 8, percentage: 8, latitude: 56.1304, longitude: -106.3468 },
        { country: "France", viewers: 6, percentage: 6, latitude: 46.6034, longitude: 1.8883 }
      ];
    } catch (err) {
      error = err.message;
      console.error("Failed to load geographic data:", err);
    }
  }

  function getCountryFlag(country) {
    const flags = {
      "United States": "🇺🇸",
      "Germany": "🇩🇪", 
      "United Kingdom": "🇬🇧",
      "Canada": "🇨🇦",
      "France": "🇫🇷"
    };
    return flags[country] || "🌍";
  }
</script>

<svelte:head>
  <title>Geographic Analytics - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">
        🌍 Geographic Analytics
      </h1>
      <p class="text-tokyo-night-fg-dark">
        View viewer distribution and regional performance metrics
      </p>
    </div>
    
    {#if streams.length > 1}
      <select
        bind:value={selectedStream}
        on:change={loadGeographicData}
        class="input"
      >
        {#each streams as stream}
          <option value={stream}>
            {stream.title || `Stream ${stream.id.slice(0, 8)}`}
          </option>
        {/each}
      </select>
    {/if}
  </div>

  {#if loading}
    <div class="flex items-center justify-center min-h-64">
      <div class="loading-spinner w-8 h-8" />
    </div>
  {:else if error}
    <div class="card border-tokyo-night-red/30">
      <div class="text-center py-12">
        <div class="text-6xl mb-4">❌</div>
        <h3 class="text-xl font-semibold text-tokyo-night-red mb-2">
          Failed to Load Geographic Data
        </h3>
        <p class="text-tokyo-night-fg-dark mb-6">{error}</p>
        <button class="btn-primary" on:click={loadGeographicData}>
          Try Again
        </button>
      </div>
    </div>
  {:else if !selectedStream}
    <div class="card text-center py-12">
      <div class="text-6xl mb-4">🎥</div>
      <h3 class="text-xl font-semibold text-tokyo-night-fg mb-2">
        No Streams Found
      </h3>
      <p class="text-tokyo-night-fg-dark mb-6">
        Create your first stream to start tracking geographic analytics
      </p>
      <a href="/streams" class="btn-primary">
        Create Stream
      </a>
    </div>
  {:else}
    <!-- World Map Placeholder -->
    <div class="glow-card p-6">
      <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
        Global Viewer Distribution
      </h2>
      
      <div class="aspect-video bg-tokyo-night-bg-dark rounded-lg flex items-center justify-center mb-6">
        <div class="text-center">
          <div class="text-6xl mb-4">🗺️</div>
          <p class="text-tokyo-night-comment">Interactive world map coming soon</p>
          <p class="text-sm text-tokyo-night-comment mt-2">
            Geographic data is being collected from MistServer
          </p>
        </div>
      </div>

      {#if geographicData.length > 0}
        <div class="grid md:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each geographicData as country}
            <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
              <div class="flex items-center justify-between mb-2">
                <div class="flex items-center space-x-2">
                  <span class="text-2xl">{getCountryFlag(country.country)}</span>
                  <span class="font-medium text-tokyo-night-fg">{country.country}</span>
                </div>
                <span class="text-sm text-tokyo-night-comment">{country.percentage}%</span>
              </div>
              <div class="flex items-center space-x-2">
                <div class="flex-1 bg-tokyo-night-bg-dark rounded-full h-2">
                  <div 
                    class="bg-tokyo-night-blue rounded-full h-2" 
                    style="width: {country.percentage}%"
                  ></div>
                </div>
                <span class="text-sm text-tokyo-night-fg">{country.viewers} viewers</span>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="text-center py-8">
          <p class="text-tokyo-night-comment">No geographic data available yet</p>
          <p class="text-sm text-tokyo-night-comment mt-2">
            Data will appear once viewers start watching your streams
          </p>
        </div>
      {/if}
    </div>

    <!-- Regional Performance -->
    <div class="glow-card p-6">
      <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
        Regional Performance
      </h2>
      
      <div class="grid md:grid-cols-2 gap-6">
        <div>
          <h3 class="text-lg font-medium text-tokyo-night-fg mb-4">
            Top Regions by Viewers
          </h3>
          <div class="space-y-3">
            {#each geographicData.slice(0, 5) as region, index}
              <div class="flex items-center justify-between">
                <div class="flex items-center space-x-3">
                  <span class="text-sm text-tokyo-night-comment w-4">#{index + 1}</span>
                  <span class="text-tokyo-night-fg">{region.country}</span>
                </div>
                <span class="text-tokyo-night-blue font-medium">{region.viewers}</span>
              </div>
            {/each}
          </div>
        </div>

        <div>
          <h3 class="text-lg font-medium text-tokyo-night-fg mb-4">
            Performance Metrics
          </h3>
          <div class="space-y-4">
            <div class="bg-tokyo-night-bg-highlight p-3 rounded-lg">
              <div class="flex justify-between items-center">
                <span class="text-sm text-tokyo-night-comment">Average Latency</span>
                <span class="text-tokyo-night-fg font-medium">Coming Soon</span>
              </div>
            </div>
            <div class="bg-tokyo-night-bg-highlight p-3 rounded-lg">
              <div class="flex justify-between items-center">
                <span class="text-sm text-tokyo-night-comment">Quality Score</span>
                <span class="text-tokyo-night-fg font-medium">Coming Soon</span>
              </div>
            </div>
            <div class="bg-tokyo-night-bg-highlight p-3 rounded-lg">
              <div class="flex justify-between items-center">
                <span class="text-sm text-tokyo-night-comment">Buffering Rate</span>
                <span class="text-tokyo-night-fg font-medium">Coming Soon</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Data Source Info -->
    <div class="p-4 bg-tokyo-night-blue/10 border border-tokyo-night-blue/30 rounded-lg">
      <div class="flex items-center space-x-2">
        <svg class="w-5 h-5 text-tokyo-night-blue" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
        </svg>
        <span class="text-tokyo-night-blue font-medium text-sm">Geographic Data Collection</span>
      </div>
      <p class="text-tokyo-night-blue/80 text-sm mt-2">
        Geographic data is collected from MistServer's client metrics API, including latitude/longitude coordinates and connection details. Interactive mapping and advanced regional analytics coming soon!
      </p>
    </div>
  {/if}
</div> 