<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetNodesStore,
    GetGeographicDistributionStore,
    GetLoadBalancingMetricsStore,
    GetRoutingEventsStore,
    GetConnectionEventsStore
  } from "$houdini";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import { NodeCard } from "$lib/components/cards";
  import CountryDistributionChart from "$lib/components/charts/CountryDistributionChart.svelte";
  import CountryTrendChart from "$lib/components/charts/CountryTrendChart.svelte";
  import GeoHeatmap from "$lib/components/charts/GeoHeatmap.svelte";
  import RoutingMap from "$lib/components/charts/RoutingMap.svelte";
  import CountryChoropleth from "$lib/components/charts/CountryChoropleth.svelte";
  import { getCountryName } from "$lib/utils/country-names";

  // Houdini stores
  const nodesStore = new GetNodesStore();
  const geoDistStore = new GetGeographicDistributionStore();
  const loadBalancingStore = new GetLoadBalancingMetricsStore();
  const routingEventsStore = new GetRoutingEventsStore();
  const connectionEventsStore = new GetConnectionEventsStore();

  // Types from Houdini
  type NodeData = NonNullable<NonNullable<typeof $nodesStore.data>["nodes"]>[0];
  type LoadBalancingMetric = NonNullable<NonNullable<typeof $loadBalancingStore.data>["loadBalancingMetrics"]>[0];
  type ConnectionEventNode = NonNullable<NonNullable<NonNullable<typeof $connectionEventsStore.data>["connectionEventsConnection"]>["edges"]>[0]["node"];

  let isAuthenticated = false;
  let loading = $derived(
    $nodesStore.fetching ||
    $geoDistStore.fetching ||
    $loadBalancingStore.fetching ||
    $connectionEventsStore.fetching
  );

  // Pagination state for connection events
  let loadingMoreEvents = $state(false);
  let hasMoreEvents = $derived($connectionEventsStore.data?.connectionEventsConnection?.pageInfo?.hasNextPage ?? false);
  let totalEventsCount = $derived($connectionEventsStore.data?.connectionEventsConnection?.totalCount ?? 0);

  // Derived data from stores
  let nodes = $derived($nodesStore.data?.nodes ?? []);
  let geographicDistribution = $derived($geoDistStore.data?.geographicDistribution ?? null);
  let loadBalancingMetrics = $derived($loadBalancingStore.data?.loadBalancingMetrics ?? []);

  // Viewer events from connection events store (with pagination support)
  let viewerEvents = $derived(
    ($connectionEventsStore.data?.connectionEventsConnection?.edges ?? []).map(e => e.node)
  );
  // Visualization mode: 'routes' shows direct client->node lines, 'buckets' shows H3 hexagons + flows
  let vizMode = $state<'routes' | 'buckets'>('routes');
  let selectedBucket: string | null = null;

  // Prepare data for Heatmap from Top Cities
  let heatmapData = $derived.by(() => {

    if (!geographicDistribution?.topCities) return [];
    
    // Normalize viewer counts to intensity 0.0 - 1.0
    // Find max viewer count to normalize
    const maxViewers = Math.max(...geographicDistribution.topCities.map(c => c.viewerCount), 1);
    
    return geographicDistribution.topCities
      .map(c => {
        if (c.latitude == null || c.longitude == null) return null;
        return {
          lat: c.latitude,
          lng: c.longitude,
          // Logarithmic scale for better visualization of outliers vs long tail
          intensity: Math.min(1.0, 0.3 + (Math.log(c.viewerCount) / Math.log(maxViewers)) * 0.7)
        };
      })
      .filter((p): p is { lat: number; lng: number; intensity: number } => p !== null);
  });

  // Prepare data for Routing Map
  // Must be $state so that $derived re-runs when h3 loads
  let cellToBoundaryFn = $state<((id: string) => [number, number][]) | null>(null);
  let cellToLatLngFn = $state<((id: string) => [number, number]) | null>(null);

  onMount(async () => {
    const h3 = await import("h3-js");
    cellToBoundaryFn = h3.cellToBoundary;
    cellToLatLngFn = h3.cellToLatLng;
    console.log('[Geographic] h3 loaded (cellToBoundary + cellToLatLng)');
  });

  const bucketToPolygon = (bucket?: { h3Index?: string | null } | null) => {
    if (!bucket?.h3Index || !cellToBoundaryFn) return null;
    try {
      const boundary = cellToBoundaryFn(bucket.h3Index);
      return boundary.map(([lat, lng]) => [lat, lng]) as [number, number][];
    } catch (e) {
      console.warn('[bucketToPolygon] Failed for h3Index:', bucket.h3Index, e);
      return null;
    }
  };

  let routingMapData = $derived.by(() => {
    // Access cellToBoundaryFn to register dependency - re-run when h3 loads
    const h3Ready = !!cellToBoundaryFn;
    const events = $routingEventsStore.data?.routingEventsConnection?.edges ?? [];

    console.log('[routingMapData] computing, h3Ready:', h3Ready, 'events:', events.length);

    // Map nodes to dictionary for easier lookup
    const nodeMap = new Map();
    nodes.forEach(n => {
      if (n.latitude && n.longitude) {
        nodeMap.set(n.nodeName, {
          id: n.id,
          name: n.nodeName,
          lat: n.latitude,
          lng: n.longitude
        });
      }
    });

    const routes: any[] = [];
    const activeNodes = new Set<string>();
    const bucketPolys: { id: string; coords: [number, number][]; kind: 'client' | 'node' }[] = [];
    const bucketSeen = new Set<string>();
    const bucketStats: Record<string, { count: number; success: number; distanceSum: number; nodeSeen: boolean }> = {};

    // Log first event to debug bucket data
    if (events.length > 0) {
      const firstEvt = events[0].node;
      console.log('[routingMapData] first event:', {
        clientBucket: firstEvt.clientBucket,
        nodeBucket: firstEvt.nodeBucket,
        clientLat: firstEvt.clientLatitude,
        clientLng: firstEvt.clientLongitude
      });
    }

    events.forEach(edge => {
      const evt = edge.node;
      // We need client lat/lng AND a resolved node lat/lng
      if (evt.clientLatitude && evt.clientLongitude) {
        let nodeLat = evt.nodeLatitude;
        let nodeLng = evt.nodeLongitude;

        // If event doesn't have node coordinates but has node name, try to look it up
        if ((!nodeLat || !nodeLng) && evt.selectedNode) {
           const nodeInfo = nodeMap.get(evt.selectedNode);
           if (nodeInfo) {
             nodeLat = nodeInfo.lat;
             nodeLng = nodeInfo.lng;
           }
        }

        if (nodeLat && nodeLng) {
          routes.push({
            from: [evt.clientLatitude, evt.clientLongitude],
            to: [nodeLat, nodeLng],
            status: evt.status,
            score: evt.score,
            details: evt.details
          });
          if (evt.selectedNode) activeNodes.add(evt.selectedNode);
        }

        // Buckets -> polygons
        const clientBucket = evt.clientBucket;
        const clientPoly = bucketToPolygon(clientBucket);
        if (clientBucket && clientPoly) {
          const id = `c-${clientBucket.h3Index}`;
          if (!bucketSeen.has(id)) {
            bucketSeen.add(id);
            bucketPolys.push({ id, coords: clientPoly, kind: 'client' });
          }
          const statKey = clientBucket.h3Index!;
          const stat = bucketStats[statKey] || { count: 0, success: 0, distanceSum: 0, nodeSeen: false };
          stat.count++;
          if (evt.status?.toLowerCase() === 'success') stat.success++;
          stat.distanceSum += evt.routingDistance ?? 0;
          if (evt.nodeBucket?.h3Index) stat.nodeSeen = true;
          bucketStats[statKey] = stat;
        }
        const nodeBucket = evt.nodeBucket;
        const nodePoly = bucketToPolygon(nodeBucket);
        if (nodeBucket && nodePoly) {
          const id = `n-${nodeBucket.h3Index}`;
          if (!bucketSeen.has(id)) {
            bucketSeen.add(id);
            bucketPolys.push({ id, coords: nodePoly, kind: 'node' });
          }
        }
      }
    });

    // Only pass nodes that are actually involved in routes or available
    const displayNodes = Array.from(nodeMap.values());

    return { routes, nodes: displayNodes, buckets: bucketPolys, bucketStats };
  });

  const bucketToCentroid = (bucket?: { h3Index?: string | null } | null) => {
    if (!bucket?.h3Index || !cellToLatLngFn) return null;
    try {
      const [lat, lng] = cellToLatLngFn(bucket.h3Index);
      return [lat, lng] as [number, number];
    } catch (e) {
      console.warn('[bucketToCentroid] Failed for h3Index:', bucket.h3Index, e);
      return null;
    }
  };

  // Simple Haversine distance in km
  function haversineDistance(lat1: number, lon1: number, lat2: number, lon2: number) {
    const R = 6371; // Radius of the earth in km
    const dLat = (lat2 - lat1) * (Math.PI / 180);
    const dLon = (lon2 - lon1) * (Math.PI / 180);
    const a =
      Math.sin(dLat / 2) * Math.sin(dLat / 2) +
      Math.cos(lat1 * (Math.PI / 180)) * Math.cos(lat2 * (Math.PI / 180)) *
      Math.sin(dLon / 2) * Math.sin(dLon / 2);
    const c = 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
    return R * c;
  }

  function resolveBucketLocation(h3Index: string, countryMap: Record<string, string>): string {
    const centroid = bucketToCentroid({ h3Index });
    // First try country map fallback if we have it
    const country = countryMap[h3Index];
    
    if (!centroid || !geographicDistribution?.topCities) {
      return country ? `${country} (${h3Index.slice(0, 6)}...)` : h3Index.slice(0, 8) + '...';
    }

    const [lat, lng] = centroid;
    let closestCity = null;
    let minDist = Infinity;

    // Find nearest city within 75km (increased from 50km)
    for (const city of geographicDistribution.topCities) {
      if (city.latitude && city.longitude) {
        const dist = haversineDistance(lat, lng, city.latitude, city.longitude);
        if (dist < minDist) {
          minDist = dist;
          closestCity = city;
        }
      }
    }

    if (closestCity && minDist < 75) {
      return `${closestCity.city}, ${closestCity.countryCode}`;
    }
    
    // Fallback to country from events
    if (country) {
      return `${getCountryName(country)} (${h3Index.slice(0, 4)}...)`;
    }
    
    return h3Index.slice(0, 8) + '...';
  }

  // Bucket hotspot list (client buckets)
  let bucketHotspots = $derived.by(() => {
    // Build bucket->country map from events
    const edges = $routingEventsStore.data?.routingEventsConnection?.edges ?? [];
    const countryMap: Record<string, string> = {};
    for (const edge of edges) {
      const evt = edge.node;
      if (evt.clientBucket?.h3Index && evt.clientCountry) {
        countryMap[evt.clientBucket.h3Index] = evt.clientCountry;
      }
    }

    const stats = routingMapData.bucketStats || {};
    const arr = Object.entries(stats)
      .map(([id, s]) => {
        // Strip prefix "c-" or "n-" to get raw index
        const rawIndex = id.includes('-') ? id.split('-')[1] : id;
        return {
          id,
          rawIndex,
          count: s.count,
          successRate: s.count ? Math.round((s.success / s.count) * 100) : 0,
          avgDistance: s.count ? s.distanceSum / s.count : 0,
          nodeSeen: s.nodeSeen,
          label: resolveBucketLocation(rawIndex, countryMap)
        };
      })
      .sort((a, b) => b.count - a.count);
    const maxCount = arr[0]?.count || 1;
    return arr.map((x) => ({ ...x, pct: Math.round((x.count / maxCount) * 100) }));
  });

  // Aggregate bucket-to-bucket flows (client -> node)
  let bucketFlows = $derived.by(() => {
    const edges = $routingEventsStore.data?.routingEventsConnection?.edges ?? [];
    const flows: Record<string, { from: string; to: string; count: number; distanceSum: number }> = {};

    for (const edge of edges) {
      const evt = edge.node;
      const from = evt.clientBucket?.h3Index;
      const to = evt.nodeBucket?.h3Index;
      if (!from || !to) continue;
      const key = `${from}->${to}`;
      if (!flows[key]) {
        flows[key] = { from, to, count: 0, distanceSum: 0 };
      }
      flows[key].count++;
      flows[key].distanceSum += evt.routingDistance ?? 0;
    }

    return Object.values(flows)
      .map(f => ({
        ...f,
        avgDistance: f.count ? f.distanceSum / f.count : 0
      }))
      .sort((a, b) => b.count - a.count);
  });



  let flowSegments = $derived.by(() => {
    return bucketFlows
      .map(flow => {
        const from = bucketToCentroid({ h3Index: flow.from });
        const to = bucketToCentroid({ h3Index: flow.to });
        if (!from || !to) return null;
        return {
          from,
          to,
          weight: Math.min(4, 1 + Math.log1p(flow.count)),
          // Purple for normal, orange for long-haul (>1500km)
          color: flow.avgDistance > 1500 ? 'rgba(249,115,22,0.65)' : 'rgba(168,85,247,0.6)'
        };
      })
      .filter(Boolean) as { from: [number, number]; to: [number, number]; weight: number; color: string }[];
  });

  let recentRoutingEvents = $derived.by(() => {
    const edges = $routingEventsStore.data?.routingEventsConnection?.edges ?? [];
    const nodes = edges.map(e => e.node);
    if (selectedBucket) {
      return nodes
        .filter(evt =>
          evt.clientBucket?.h3Index === selectedBucket ||
          evt.nodeBucket?.h3Index === selectedBucket
        )
        .slice(0, 12);
    }
    return nodes.slice(0, 12);
  });

  const formatBucketId = (bucket?: { h3Index?: string | null } | null) =>
    bucket?.h3Index ? bucket.h3Index.slice(0, 6) + '…' : '—';

  // Routing efficiency calculated from routing events
  interface RoutingEfficiency {
    efficiency: number;
    avgScore: number;
    totalDecisions: number;
    avgDistance: number;
  }

  let routingEfficiency = $derived.by((): RoutingEfficiency & { avgLatency: number } => {
    const routingEvents = $routingEventsStore.data?.routingEventsConnection?.edges ?? [];
    if (routingEvents.length === 0) {
      return { efficiency: 0, avgScore: 0, totalDecisions: 0, avgDistance: 0, avgLatency: 0 };
    }

    let successCount = 0;
    let totalScore = 0;
    let totalDistance = 0;

    for (const edge of routingEvents) {
      const event = edge.node;
      if (event.selectedNode) successCount++;
      totalScore += event.score ?? 0;
      totalDistance += event.routingDistance ?? 0;
    }

    // Calculate avg latency from load balancing metrics (has latencyMs)
    const lbMetrics = loadBalancingMetrics;
    let totalLatency = 0;
    let latencyCount = 0;
    for (const metric of lbMetrics) {
      if (metric.latencyMs) {
        totalLatency += metric.latencyMs;
        latencyCount++;
      }
    }

    return {
      efficiency: (successCount / routingEvents.length) * 100,
      avgScore: totalScore / routingEvents.length,
      totalDecisions: routingEvents.length,
      avgDistance: totalDistance / routingEvents.length,
      avgLatency: latencyCount > 0 ? totalLatency / latencyCount : 0,
    };
  });

  // Connection patterns calculated from connection events
  interface ConnectionPatterns {
    totalConnections: number;
    totalDisconnections: number;
    netConnections: number;
    countryDistribution: Record<string, number>;
    nodeDistribution: Record<string, number>;
  }

  let connectionPatterns = $derived.by((): ConnectionPatterns => {
    const connectionEdges = $connectionEventsStore.data?.connectionEventsConnection?.edges ?? [];
    let totalConnections = 0;
    let totalDisconnections = 0;
    const countryDistribution: Record<string, number> = {};
    const nodeDistribution: Record<string, number> = {};

    for (const edge of connectionEdges) {
      const event = edge.node;
      if (event.eventType === 'connect') {
        totalConnections++;
      } else if (event.eventType === 'disconnect') {
        totalDisconnections++;
      }
      if (event.countryCode) {
        countryDistribution[event.countryCode] = (countryDistribution[event.countryCode] || 0) + 1;
      }
      if (event.nodeId) {
        nodeDistribution[event.nodeId] = (nodeDistribution[event.nodeId] || 0) + 1;
      }
    }

    return {
      totalConnections,
      totalDisconnections,
      netConnections: totalConnections - totalDisconnections,
      countryDistribution,
      nodeDistribution,
    };
  });

  // Helper functions for formatting session data
  function formatDuration(seconds: number): string {
    if (seconds < 60) return `${seconds}s`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    return `${h}h ${m}m`;
  }

  function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }

  let error = $state<string | null>(null);

  const timeRange = {
    start: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    end: new Date().toISOString()
  };

  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadAllData();
  });

  async function loadAllData() {
    try {
      error = null;

      await Promise.all([
        nodesStore.fetch(),
        geoDistStore.fetch({ variables: { stream: null, timeRange } }),
        loadBalancingStore.fetch({ variables: { timeRange } }),
        routingEventsStore.fetch({ variables: { stream: null, timeRange } }),
        connectionEventsStore.fetch({ variables: { timeRange } }),
      ]);

      if ($nodesStore.errors?.length) {
        error = $nodesStore.errors[0].message;
        console.error("Failed to load node data:", $nodesStore.errors);
      }
      if ($geoDistStore.errors?.length) {
        console.error("Failed to load geographic distribution:", $geoDistStore.errors);
      }
      if ($loadBalancingStore.errors?.length) {
        console.error("Failed to load load balancing metrics:", $loadBalancingStore.errors);
      }
    } catch (err: any) {
      error = err.message || "Failed to load data";
      console.error("Failed to load data:", err);
    }
  }

  // Load more connection events (pagination)
  async function loadMoreEvents() {
    if (!hasMoreEvents || loadingMoreEvents) return;
    try {
      loadingMoreEvents = true;
      await connectionEventsStore.loadNextPage({ first: 50 });
    } catch (err) {
      console.error("Failed to load more events:", err);
    } finally {
      loadingMoreEvents = false;
    }
  }

  // Popular routing targets: count successful routing decisions by selected node
  let mostPopularNodes = $derived.by(() => {
    const routingEdges = $routingEventsStore.data?.routingEventsConnection?.edges ?? [];
    const nodeRouteCounts: Record<string, number> = {};

    for (const edge of routingEdges) {
      const event = edge.node;
      // Only count successful routing decisions with a selected node
      const status = event.status?.toLowerCase();
      if (event.selectedNode && (status === 'success' || status === 'routed')) {
        nodeRouteCounts[event.selectedNode] = (nodeRouteCounts[event.selectedNode] || 0) + 1;
      }
    }

    return Object.entries(nodeRouteCounts)
      .map(([nodeName, count]) => ({ nodeName, connectionCount: count }))
      .sort((a, b) => b.connectionCount - a.connectionCount);
  });

  let connectionQualityByDistance = $derived.by(() => {
    const shortRange = { total: 0, success: 0 };
    const mediumRange = { total: 0, success: 0 };
    const longRange = { total: 0, success: 0 };

    for (const metric of loadBalancingMetrics) {
      const distance = metric.routingDistance || 0;
      const isSuccess = metric.status === 'success' || metric.status === 'SUCCESS';

      if (distance < 500) {
        shortRange.total++;
        if (isSuccess) shortRange.success++;
      } else if (distance <= 2000) {
        mediumRange.total++;
        if (isSuccess) mediumRange.success++;
      } else {
        longRange.total++;
        if (isSuccess) longRange.success++;
      }
    }

    return {
      short: shortRange.total > 0 ? Math.round((shortRange.success / shortRange.total) * 100) : 0,
      medium: mediumRange.total > 0 ? Math.round((mediumRange.success / mediumRange.total) * 100) : 0,
      long: longRange.total > 0 ? Math.round((longRange.success / longRange.total) * 100) : 0,
      hasData: loadBalancingMetrics.length > 0
    };
  });

  // Icons
  const GlobeIcon = getIconComponent('Globe');
  const Globe2Icon = getIconComponent('Globe2');
  const UsersIcon = getIconComponent('Users');
  const MapPinIcon = getIconComponent('Target');
  const ActivityIcon = getIconComponent('Activity');
  const AlertCircleIcon = getIconComponent('AlertCircle');
  const ServerIcon = getIconComponent('Server');
  const ZapIcon = getIconComponent('Zap');
  const ChartLineIcon = getIconComponent('ChartLine');
  const MonitorIcon = getIconComponent('Monitor');
</script>

<svelte:head>
  <title>Geographic Analytics - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center gap-3">
      <Globe2Icon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Geographic</h1>
        <p class="text-sm text-muted-foreground">
          Viewer distribution, infrastructure nodes, and geographic load balancing
        </p>
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
        <h3 class="text-lg font-semibold text-destructive mb-2">Failed to Load Data</h3>
        <p class="text-muted-foreground mb-6">{error}</p>
        <Button onclick={loadAllData}>Try Again</Button>
      </div>
    </div>
  {:else}
    <div class="page-transition">

      <!-- Geographic Overview Stats -->
      {#if geographicDistribution}
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
          <div>
            <DashboardMetricCard
              icon={UsersIcon}
              iconColor="text-success"
              value={geographicDistribution.totalViewers}
              valueColor="text-success"
              label="Total Viewers"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={Globe2Icon}
              iconColor="text-primary"
              value={geographicDistribution.uniqueCountries}
              valueColor="text-primary"
              label="Countries"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={MapPinIcon}
              iconColor="text-accent-purple"
              value={geographicDistribution.uniqueCities}
              valueColor="text-accent-purple"
              label="Cities"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={ActivityIcon}
              iconColor="text-warning"
              value={loadBalancingMetrics.length}
              valueColor="text-warning"
              label="Routing Events"
            />
          </div>
        </GridSeam>
      {/if}

      <!-- Main Content Grid -->
      <div class="dashboard-grid">
        <!-- Global Viewer Heatmap Slab -->
        {#if geographicDistribution?.topCities?.length}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <Globe2Icon class="w-4 h-4 text-primary" />
                <h3>Global Viewer Heatmap</h3>
              </div>
            </div>
            <div class="slab-body--flush h-[400px]">
              {#if typeof window !== 'undefined'}
                <GeoHeatmap data={heatmapData} height={400} />
              {/if}
            </div>
          </div>
        {/if}

        <!-- Country Choropleth Slab -->
        {#if geographicDistribution?.topCountries?.length}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <GlobeIcon class="w-4 h-4 text-primary" />
                <h3>Country Choropleth</h3>
              </div>
            </div>
            <div class="slab-body--flush h-[320px]">
              {#if typeof window !== 'undefined'}
                <CountryChoropleth data={geographicDistribution.topCountries} height={320} />
              {/if}
            </div>
          </div>
        {/if}

        <!-- Country Distribution Slab -->
        {#if geographicDistribution?.topCountries?.length}
          <div class="slab xl:col-span-2">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <Globe2Icon class="w-4 h-4 text-primary" />
                <h3>Viewer Distribution by Country</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="p-4 border border-border/30 bg-muted/20">
                <CountryDistributionChart
                  data={geographicDistribution.topCountries}
                  height={280}
                  maxItems={8}
                />
              </div>
            </div>

          </div>

          <!-- Top Countries & Cities Slab -->
          <div class="slab">
            <div class="slab-header">
              <h3>Top Locations</h3>
            </div>
            <div class="slab-body--padded space-y-4">
              <!-- Top Countries -->
              <div>
                <p class="text-xs text-muted-foreground uppercase tracking-wide mb-2">Countries</p>
                <div class="space-y-2">
                  {#each geographicDistribution.topCountries.slice(0, 5) as country (country.countryCode)}
                    <div class="flex justify-between items-center p-2 rounded border border-border/30 bg-muted/20">
                      <span class="text-sm">{getCountryName(country.countryCode)}</span>
                      <div class="text-right">
                        <span class="font-semibold text-foreground">{country.viewerCount}</span>
                        <span class="text-xs text-muted-foreground ml-1">({country.percentage.toFixed(1)}%)</span>
                      </div>
                    </div>
                  {/each}
                </div>
              </div>
              <!-- Top Cities -->
              <div class="pt-3 border-t border-border/30">
                <p class="text-xs text-muted-foreground uppercase tracking-wide mb-2">Cities</p>
                <div class="space-y-2">
                  {#each geographicDistribution.topCities.slice(0, 4) as city (`${city.city}-${city.countryCode}`)}
                    <div class="flex justify-between items-center p-2 rounded border border-border/30 bg-muted/20">
                      <div>
                        <span class="text-sm text-foreground">{city.city}</span>
                        <span class="text-xs text-muted-foreground ml-1">({getCountryName(city.countryCode ?? '')})</span>
                      </div>
                      <span class="font-semibold text-foreground">{city.viewerCount}</span>
                    </div>
                  {/each}
                </div>
              </div>
            </div>
          </div>
        {/if}

        <!-- Country Trend Slab -->
        {#if geographicDistribution?.viewersByCountry?.length}
          <div class="slab col-span-full">
            <div class="slab-header">
              <h3>Viewer Trends by Country (Over Time)</h3>
            </div>
            <div class="slab-body--padded">
              <div class="p-4 border border-border/30 bg-muted/20">
                <CountryTrendChart
                  data={geographicDistribution.viewersByCountry}
                  height={300}
                  maxCountries={6}
                />
              </div>
            </div>
          </div>
        {/if}

        <!-- Routing Efficiency Slab -->
        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <ActivityIcon class="w-4 h-4 text-success" />
              <h3>Routing Efficiency</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            <div class="grid grid-cols-2 gap-3 mb-4">
              <div class="p-3 text-center border border-border/30 bg-muted/20">
                <p class="text-xs text-muted-foreground uppercase mb-1">Success Rate</p>
                <p class="text-xl font-bold text-success">{routingEfficiency.efficiency?.toFixed(1) || 0}%</p>
              </div>
              <div class="p-3 text-center border border-border/30 bg-muted/20">
                <p class="text-xs text-muted-foreground uppercase mb-1">Avg Score</p>
                <p class="text-xl font-bold text-primary">{routingEfficiency.avgScore?.toFixed(1) || 0}</p>
              </div>
              <div class="p-3 text-center border border-border/30 bg-muted/20">
                <p class="text-xs text-muted-foreground uppercase mb-1">Decisions</p>
                <p class="text-xl font-bold text-accent-purple">{routingEfficiency.totalDecisions || 0}</p>
              </div>
              <div class="p-3 text-center border border-border/30 bg-muted/20">
                <p class="text-xs text-muted-foreground uppercase mb-1">Avg Distance</p>
                <p class="text-xl font-bold text-warning">{routingEfficiency.avgDistance?.toFixed(0) || 0}km</p>
              </div>
            </div>
            {#if routingEfficiency.avgLatency > 0}
              <div class="p-3 border border-info/30 bg-info/5 mb-4">
                <div class="flex items-center justify-between">
                  <div>
                    <p class="text-xs text-muted-foreground uppercase mb-1">Routing Latency</p>
                    <p class="text-lg font-bold text-info">{routingEfficiency.avgLatency.toFixed(1)}ms</p>
                  </div>
                  <div class="text-right text-xs text-muted-foreground">
                    <p>Time to select optimal node</p>
                    <p class="font-mono {routingEfficiency.avgLatency < 50 ? 'text-success' : routingEfficiency.avgLatency < 100 ? 'text-warning' : 'text-destructive'}">
                      {routingEfficiency.avgLatency < 50 ? 'Excellent' : routingEfficiency.avgLatency < 100 ? 'Good' : 'Needs attention'}
                    </p>
                  </div>
                </div>
              </div>
            {/if}

            <!-- Connection Quality by Distance -->
            {#if connectionQualityByDistance.hasData}
              {@const quality = connectionQualityByDistance}
              <div class="pt-3 border-t border-border/30">
                <p class="text-xs text-muted-foreground uppercase tracking-wide mb-3">Quality by Distance</p>
                <div class="space-y-2">
                  <div class="flex justify-between items-center text-sm">
                    <span class="text-muted-foreground">&lt;500km</span>
                    <div class="flex items-center gap-2">
                      <div class="w-20 bg-muted rounded-full h-1.5">
                        <div class="h-1.5 rounded-full {quality.short >= 80 ? 'bg-success' : quality.short >= 60 ? 'bg-warning' : 'bg-destructive'}" style="width: {quality.short}%"></div>
                      </div>
                      <span class="text-xs font-mono w-10 text-right {quality.short >= 80 ? 'text-success' : quality.short >= 60 ? 'text-warning' : 'text-destructive'}">{quality.short}%</span>
                    </div>
                  </div>
                  <div class="flex justify-between items-center text-sm">
                    <span class="text-muted-foreground">500-2000km</span>
                    <div class="flex items-center gap-2">
                      <div class="w-20 bg-muted rounded-full h-1.5">
                        <div class="h-1.5 rounded-full {quality.medium >= 80 ? 'bg-success' : quality.medium >= 60 ? 'bg-warning' : 'bg-destructive'}" style="width: {quality.medium}%"></div>
                      </div>
                      <span class="text-xs font-mono w-10 text-right {quality.medium >= 80 ? 'text-success' : quality.medium >= 60 ? 'text-warning' : 'text-destructive'}">{quality.medium}%</span>
                    </div>
                  </div>
                  <div class="flex justify-between items-center text-sm">
                    <span class="text-muted-foreground">&gt;2000km</span>
                    <div class="flex items-center gap-2">
                      <div class="w-20 bg-muted rounded-full h-1.5">
                        <div class="h-1.5 rounded-full {quality.long >= 80 ? 'bg-success' : quality.long >= 60 ? 'bg-warning' : 'bg-destructive'}" style="width: {quality.long}%"></div>
                      </div>
                      <span class="text-xs font-mono w-10 text-right {quality.long >= 80 ? 'text-success' : quality.long >= 60 ? 'text-warning' : 'text-destructive'}">{quality.long}%</span>
                    </div>
                  </div>
                </div>
              </div>
            {/if}
          </div>
        </div>

        <!-- Routing Map Slab -->
        {#if routingMapData.routes.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center justify-between w-full">
                <div class="flex items-center gap-3">
                  <ActivityIcon class="w-4 h-4 text-primary" />
                  <h3>Routing Spider Map</h3>
                </div>
                <!-- Visualization Mode Toggle -->
                <div class="flex items-center gap-1 p-0.5 bg-muted/50 rounded">
                  <button
                    class="px-3 py-1 text-xs font-medium rounded transition-colors {vizMode === 'routes' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'}"
                    onclick={() => vizMode = 'routes'}
                  >
                    Routes
                  </button>
                  <button
                    class="px-3 py-1 text-xs font-medium rounded transition-colors {vizMode === 'buckets' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'}"
                    onclick={() => vizMode = 'buckets'}
                  >
                    Buckets
                  </button>
                </div>
              </div>
            </div>
            <div class="slab-body--flush h-[500px]">
              {#if typeof window !== 'undefined'}
                <RoutingMap
                  routes={vizMode === 'routes' ? routingMapData.routes : []}
                  nodes={routingMapData.nodes}
                  buckets={vizMode === 'buckets' ? routingMapData.buckets : []}
                  flows={vizMode === 'buckets' ? flowSegments : []}
                  onBucketClick={(id) => {
                    const clean = id.slice(2); // drop prefix c-/n-
                    selectedBucket = selectedBucket === clean ? null : clean;
                  }}
                  height={500}
                />
              {/if}
            </div>
          </div>
        {/if}

        <!-- Map Legend (mode-dependent) -->
        {#if routingMapData.routes.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <Globe2Icon class="w-4 h-4 text-muted-foreground" />
                <h3>Map Legend</h3>
              </div>
            </div>
            <div class="slab-body--padded text-xs text-muted-foreground space-y-3">
              {#if vizMode === 'routes'}
                <!-- Routes Mode Legend -->
                <div class="space-y-2">
                  <p class="text-[11px] uppercase tracking-wide text-muted-foreground/70 font-medium">Direct Routing View</p>
                  <div class="flex items-center gap-3">
                    <span class="inline-block w-3 h-3 rounded-full bg-primary shadow-[0_0_6px_theme(colors.primary)]"></span>
                    <span>Edge Node (infrastructure server)</span>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="inline-block w-2 h-2 rounded-full bg-success"></span>
                    <span>Client origin (viewer location)</span>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="inline-block w-6 h-0.5 bg-success/60"></span>
                    <span>Successful route (client → node)</span>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="inline-block w-6 h-0.5 bg-destructive/60"></span>
                    <span>Failed route</span>
                  </div>
                </div>
              {:else}
                <!-- Buckets Mode Legend -->
                <div class="space-y-2">
                  <p class="text-[11px] uppercase tracking-wide text-muted-foreground/70 font-medium">Aggregated Bucket View (H3 Resolution 5, ~25km hexagons)</p>
                  <div class="flex items-center gap-3">
                    <span class="inline-block w-4 h-4 rounded-sm bg-primary/30 border border-primary/50"></span>
                    <span>Client bucket (viewer region)</span>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="inline-block w-4 h-4 rounded-sm bg-success/30 border border-success/50"></span>
                    <span>Node bucket (edge server region)</span>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="inline-block w-6 h-0.5 bg-purple-500/60"></span>
                    <span>Bucket flow (&lt;1500km average distance)</span>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="inline-block w-6 h-0.5 bg-orange-500/60"></span>
                    <span>Long-haul flow (&gt;1500km) - consider adding closer edge</span>
                  </div>
                  {#if bucketHotspots.length > 0}
                    {@const counts = bucketHotspots.map((b) => b.count)}
                    {@const minCount = Math.min(...counts)}
                    {@const maxCount = Math.max(...counts)}
                    {@const midCount = counts[Math.floor(counts.length / 2)]}
                    <div class="pt-2 border-t border-border/30">
                      <p class="text-[11px] text-muted-foreground/70 mb-2">Bucket fill intensity (by event count):</p>
                      <div class="flex items-center gap-2 text-[11px] text-muted-foreground">
                        <span>{minCount}</span>
                        <div class="flex-1 h-2 rounded bg-gradient-to-r from-primary/20 via-primary/50 to-primary/80"></div>
                        <span>{midCount}</span>
                        <div class="flex-1 h-2 rounded bg-gradient-to-r from-primary/80 via-success/60 to-success/90"></div>
                        <span>{maxCount} events</span>
                      </div>
                    </div>
                  {/if}
                  <p class="text-[11px] text-muted-foreground/80 pt-1">Click a bucket to filter tables below; click again to clear.</p>
                </div>
              {/if}
            </div>
          </div>
        {/if}

        <!-- Bucket Hotspots Slab -->
        {#if bucketHotspots.length > 0}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <MapPinIcon class="w-4 h-4 text-primary" />
                <h3>Top Buckets (Clients)</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-2">
                {#each bucketHotspots.slice(0, 8) as b (b.id)}
                  <div class="flex items-center justify-between p-2 border border-border/30 bg-muted/10">
                    <div class="flex items-center gap-2">
                      <span class="font-mono text-xs px-2 py-0.5 bg-primary/10 text-primary rounded" title={b.id}>{b.label}</span>
                      <span class="text-muted-foreground text-xs">{b.pct}%</span>
                    </div>
                    <div class="flex items-center gap-3 text-xs">
                      <span class="font-semibold text-foreground">{b.count} events</span>
                      <span class="{b.successRate >= 80 ? 'text-success' : b.successRate >= 60 ? 'text-warning' : 'text-destructive'}">
                        {b.successRate}% success
                      </span>
                      <span class="text-muted-foreground">· {Math.round(b.avgDistance)} km</span>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          </div>
        {/if}

        <!-- Distance Hotspots Slab -->
        {#if bucketHotspots.length > 0}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <ActivityIcon class="w-4 h-4 text-warning" />
                <h3>Distance Hotspots</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-2">
                {#each bucketHotspots
                  .filter(b => b.avgDistance > 0)
                  .sort((a, b) => b.avgDistance - a.avgDistance)
                  .slice(0, 6) as b (b.id)}
                  <div class="flex items-center justify-between p-2 border border-border/30 bg-muted/10">
                    <div class="flex items-center gap-2">
                      <span class="font-mono text-xs px-2 py-0.5 bg-warning/10 text-warning rounded" title={b.id}>{b.label}</span>
                      <span class="text-muted-foreground text-xs">{Math.round(b.avgDistance)} km avg</span>
                    </div>
                    <div class="text-xs text-muted-foreground flex items-center gap-2">
                      <span>{b.count} events</span>
                      {#if b.avgDistance > 1500}
                        <span class="px-2 py-0.5 rounded bg-destructive/10 text-destructive font-mono">long-haul</span>
                      {:else if b.avgDistance > 800}
                        <span class="px-2 py-0.5 rounded bg-warning/10 text-warning font-mono">elevated</span>
                      {/if}
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          </div>
        {/if}

        <!-- Top Bucket Flows -->
        {#if bucketFlows.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <ActivityIcon class="w-4 h-4 text-accent-purple" />
                <h3>Top Bucket Flows</h3>
              </div>
            </div>
            <div class="slab-body--flush">
              <div class="overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b border-border bg-muted/30">
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">From</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">To</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Events</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Avg Distance</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Badge</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each bucketFlows.slice(0, 10) as flow (flow.from + flow.to)}
                      <tr class="border-b border-border/30 hover:bg-muted/15">
                        <td class="py-2 px-4 font-mono text-xs">{flow.from.slice(0,6)}…</td>
                        <td class="py-2 px-4 font-mono text-xs">{flow.to.slice(0,6)}…</td>
                        <td class="py-2 px-4 font-semibold text-foreground">{flow.count}</td>
                        <td class="py-2 px-4 text-xs text-muted-foreground">{Math.round(flow.avgDistance)} km</td>
                        <td class="py-2 px-4">
                          {#if flow.avgDistance > 1500}
                            <span class="px-2 py-0.5 rounded bg-destructive/15 text-destructive text-[11px] font-mono">long-haul</span>
                          {:else if flow.avgDistance > 800}
                            <span class="px-2 py-0.5 rounded bg-warning/15 text-warning text-[11px] font-mono">elevated</span>
                          {:else}
                            <span class="px-2 py-0.5 rounded bg-success/10 text-success text-[11px] font-mono">localish</span>
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        {/if}

        <!-- Coverage Gaps Slab -->
        {#if bucketHotspots.some(b => !b.nodeSeen)}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <AlertCircleIcon class="w-4 h-4 text-destructive" />
                <h3>Coverage Gaps (no node bucket seen)</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-2">
                {#each bucketHotspots.filter(b => !b.nodeSeen).slice(0, 6) as b (b.id)}
                  <div class="flex items-center justify-between p-2 border border-destructive/20 bg-destructive/5">
                    <div class="flex items-center gap-2">
                      <span class="font-mono text-xs px-2 py-0.5 bg-destructive/20 text-destructive rounded" title={b.id}>{b.label}</span>
                      <span class="text-muted-foreground text-xs">{b.count} events</span>
                    </div>
                    <Button size="sm" variant="ghost" class="text-xs" onclick={() => { selectedBucket = b.id; }}>
                      Focus
                    </Button>
                  </div>
                {/each}
              </div>
              {#if bucketHotspots.filter(b => !b.nodeSeen).length > 6}
                <p class="text-[11px] text-muted-foreground mt-2">Showing top 6 coverage gaps.</p>
              {/if}
            </div>
          </div>
        {/if}

        <!-- Recent Routing Events Slab -->
        {#if recentRoutingEvents.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <ActivityIcon class="w-4 h-4 text-info" />
                <h3>Recent Routing Decisions</h3>
              </div>
            </div>
            <div class="slab-body--flush">
              <div class="overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b border-border bg-muted/30">
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Time</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Stream</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Client Bucket</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Node Bucket</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Selected Node</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each recentRoutingEvents as evt (evt.timestamp + evt.selectedNode)}
                      <tr class="border-b border-border/30 hover:bg-muted/15">
                        <td class="py-2 px-4 text-xs text-muted-foreground">{new Date(evt.timestamp).toLocaleTimeString()}</td>
                        <td class="py-2 px-4 font-mono text-xs">{evt.streamName}</td>
                        <td class="py-2 px-4 font-mono text-xs">{formatBucketId(evt.clientBucket)}</td>
                        <td class="py-2 px-4 font-mono text-xs">{formatBucketId(evt.nodeBucket)}</td>
                        <td class="py-2 px-4 font-mono text-xs">{evt.selectedNode}</td>
                        <td class="py-2 px-4">
                          <span class="px-2 py-0.5 rounded text-xs font-mono {evt.status === 'success' || evt.status === 'SUCCESS' ? 'bg-success/20 text-success' : 'bg-destructive/20 text-destructive'}">
                            {evt.status}
                          </span>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        {/if}

        <!-- Popular Nodes Slab -->
        {#if mostPopularNodes.length > 0}
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <ServerIcon class="w-4 h-4 text-info" />
                <h3>Popular Routing Targets</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-2">
                {#each mostPopularNodes.slice(0, 6) as node (node.nodeName)}
                  <div class="flex justify-between items-center p-2 rounded border border-border/30 bg-muted/20">
                    <span class="font-mono text-sm">{node.nodeName}</span>
                    <span class="font-semibold text-primary">{node.connectionCount}</span>
                  </div>
                {/each}
              </div>
            </div>
            <div class="slab-actions">
              <Button href={resolve("/infrastructure")} variant="ghost" class="gap-2">
                <ServerIcon class="w-4 h-4" />
                View Infrastructure
              </Button>
            </div>
          </div>
        {/if}

        <!-- Viewer Connection Events Slab -->
        {#if viewerEvents.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center justify-between w-full">
                <h3>Viewer Connection Events</h3>
                <span class="text-xs text-muted-foreground">
                  {viewerEvents.length} of {totalEventsCount} events
                </span>
              </div>
            </div>
            <div class="slab-body--flush">
              <div class="overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b border-border bg-muted/30">
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Time</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Event</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Stream</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Location (centroid)</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Node</th>
                      <th class="text-left py-2 px-4 text-muted-foreground font-medium">Details</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each viewerEvents as event (event.eventId)}
                      <tr class="border-b border-border/30 hover:bg-muted/20">
                        <td class="py-2 px-4 text-xs text-muted-foreground">{new Date(event.timestamp).toLocaleTimeString()}</td>
                        <td class="py-2 px-4">
                          {#if event.eventType}
                            <span class="px-2 py-0.5 rounded text-xs font-mono {event.eventType === 'connect' ? 'bg-success/20 text-success' : event.eventType === 'disconnect' ? 'bg-destructive/20 text-destructive' : 'bg-primary/20 text-primary'}">
                              {event.eventType}
                            </span>
                          {:else}
                            <span class="text-muted-foreground text-xs">-</span>
                          {/if}
                        </td>
                        <td class="py-2 px-4 font-mono text-xs">{event.internalName || '-'}</td>
                        <td class="py-2 px-4">
                          {#if event.city || event.countryCode}
                            <span class="text-foreground text-xs">{event.city || ''}</span>
                            {#if event.countryCode}
                              <span class="text-[10px] text-muted-foreground ml-1">({getCountryName(event.countryCode)})</span>
                            {/if}
                          {:else}
                            <span class="text-muted-foreground text-xs">Unknown</span>
                          {/if}
                        </td>
                        <td class="py-2 px-4 font-mono text-xs">{event.nodeId || '-'}</td>
                        <td class="py-2 px-4">
                          {#if event.eventType === 'disconnect' && event.sessionDurationSeconds}
                            <span class="text-xs text-foreground">
                              {formatDuration(event.sessionDurationSeconds)}
                            </span>
                            {#if event.bytesTransferred}
                              <span class="text-[10px] text-muted-foreground ml-1">
                                ({formatBytes(event.bytesTransferred)})
                              </span>
                            {/if}
                          {:else if event.eventType === 'connect' && event.connector}
                            <span class="px-1.5 py-0.5 rounded text-[10px] bg-primary/10 text-primary font-mono">
                              {event.connector}
                            </span>
                          {:else}
                            <span class="text-muted-foreground text-xs">—</span>
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
              {#if hasMoreEvents}
                <div class="flex justify-center py-3 border-t border-border/30">
                  <Button
                    variant="outline"
                    size="sm"
                    onclick={loadMoreEvents}
                    disabled={loadingMoreEvents}
                  >
                    {#if loadingMoreEvents}
                      Loading...
                    {:else}
                      Load More Events
                    {/if}
                  </Button>
                </div>
              {:else if viewerEvents.length > 0}
                <p class="text-xs text-muted-foreground text-center py-3 border-t border-border/30">
                  All {totalEventsCount} events loaded
                </p>
              {/if}
            </div>
          </div>
        {/if}


      </div>
    </div>
  {/if}
  </div>
</div>
