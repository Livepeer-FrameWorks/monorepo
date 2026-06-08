<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { SvelteURLSearchParams } from "svelte/reactivity";
  import {
    GetPlayerBootSummaryStore,
    GetClusterBootOpsStore,
    GetPlayerBootTimeSeriesStore,
    GetSessionQoeSummaryStore,
    GetClusterQoeOpsStore,
    GetSessionQoeTimeSeriesStore,
    ListVodRetentionAssetsStore,
    GetVodRetentionStore,
    type GetPlayerBootSummary$result,
    type GetPlayerBootTimeSeries$result,
    type GetClusterBootOps$result,
    type GetSessionQoeSummary$result,
    type GetSessionQoeTimeSeries$result,
    type GetClusterQoeOps$result,
    type ListVodRetentionAssets$result,
    type GetVodRetention$result,
  } from "$houdini";
  import { auth } from "$lib/stores/auth";
  import { toast } from "$lib/stores/toast.js";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Tabs, TabsContent, TabsList, TabsTrigger } from "$lib/components/ui/tabs";
  import { Badge } from "$lib/components/ui/badge";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import BootTtfTrendChart from "$lib/components/charts/BootTtfTrendChart.svelte";
  import SessionQoeTrendChart from "$lib/components/charts/SessionQoeTrendChart.svelte";
  import VodRetentionChart from "$lib/components/charts/VodRetentionChart.svelte";
  import VodRetentionAssetPicker from "$lib/components/charts/VodRetentionAssetPicker.svelte";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { getIconComponent } from "$lib/iconUtils";

  const ASSET_PAGE_SIZE = 20;

  const bootSummaryStore = new GetPlayerBootSummaryStore();
  const bootSeriesStore = new GetPlayerBootTimeSeriesStore();
  const clusterBootStore = new GetClusterBootOpsStore();
  const qoeSummaryStore = new GetSessionQoeSummaryStore();
  const qoeSeriesStore = new GetSessionQoeTimeSeriesStore();
  const clusterQoeStore = new GetClusterQoeOpsStore();
  const assetsStore = new ListVodRetentionAssetsStore();
  const retentionStore = new GetVodRetentionStore();

  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS;

  type TabId = "startup" | "playback" | "retention";
  type BootSummary = GetPlayerBootSummary$result["analytics"]["health"]["playerBootSummary"];
  type BootSeries = GetPlayerBootTimeSeries$result["analytics"]["health"]["playerBootTimeSeries"];
  type BootClusterRows = GetClusterBootOps$result["analytics"]["infra"]["clusterBootOps"];
  type QoeSummary = GetSessionQoeSummary$result["analytics"]["health"]["sessionQoeSummary"];
  type QoeSeries = GetSessionQoeTimeSeries$result["analytics"]["health"]["sessionQoeTimeSeries"];
  type QoeClusterRows = GetClusterQoeOps$result["analytics"]["infra"]["clusterQoeOps"];
  type AssetsConnection =
    ListVodRetentionAssets$result["analytics"]["health"]["vodRetentionAssets"];
  type Retention = GetVodRetention$result["analytics"]["health"]["vodRetention"];

  let activeTab = $state<TabId>("startup");
  let selectedAsset = $state<string | null>(null);
  // Each tab loads on first view and re-loads when the range changes; a new range
  // resets loadedTabs so only the visible tab refetches and the rest lazy-load on open.
  let loadedTabs = $state<Record<TabId, boolean>>({
    startup: false,
    playback: false,
    retention: false,
  });
  let tabLoading = $state(true);
  let retentionLoading = $state(false);
  let assetsLoading = $state(false);
  let assetCursor = $state<{ after?: string; before?: string }>({});
  let rangeEpoch = 0;
  let tabLoadToken = 0;
  let assetsLoadToken = 0;
  let retentionLoadToken = 0;

  let bootSummary = $state<BootSummary | null>(null);
  let bootSeries = $state<BootSeries>([]);
  let bootClusterRows = $state<BootClusterRows>([]);
  let qoeSummary = $state<QoeSummary | null>(null);
  let qoeSeries = $state<QoeSeries>([]);
  let qoeClusterRows = $state<QoeClusterRows>([]);
  let assetsConn = $state<AssetsConnection | null>(null);
  let assets = $derived(assetsConn?.nodes ?? []);
  let hasVisitedRetentionAssetPage = $derived(Boolean(assetCursor.after || assetCursor.before));
  let canPageRetentionAssetsBackward = $derived(
    hasVisitedRetentionAssetPage && (assetsConn?.pageInfo?.hasPreviousPage ?? false)
  );
  let retention = $state<Retention | null>(null);

  const RocketIcon = getIconComponent("Rocket");
  const ActivityIcon = getIconComponent("Activity");
  const CalendarIcon = getIconComponent("Calendar");
  const ZapIcon = getIconComponent("Zap");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");
  const PauseIcon = getIconComponent("Pause");
  const GaugeIcon = getIconComponent("Gauge");
  const FilmIcon = getIconComponent("Film");

  function ms(value: number | undefined | null): string {
    if (value === undefined || value === null) return "—";
    return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${Math.round(value)}ms`;
  }
  function pct(value: number | undefined | null, digits = 2): string {
    if (value === undefined || value === null) return "—";
    return `${(value * 100).toFixed(digits)}%`;
  }
  function num(value: number | undefined | null, digits = 1): string {
    if (value === undefined || value === null) return "—";
    return value.toFixed(digits);
  }
  function mbps(bps: number | undefined | null): string {
    if (!bps) return "—";
    return `${(bps / 1_000_000).toFixed(2)} Mbps`;
  }

  // Coarser ranges get coarser buckets; 24h stays at 15m so QoE spikes are visible.
  function intervalForRange(value: string): string {
    switch (value) {
      case "24h":
        return "15m";
      case "7d":
        return "1h";
      case "30d":
        return "1d";
      case "90d":
        return "1d";
      default:
        return "1h";
    }
  }

  function syncUrl() {
    const params = new SvelteURLSearchParams();
    if (activeTab !== "startup") params.set("tab", activeTab);
    if (activeTab === "retention" && selectedAsset) params.set("asset", selectedAsset);
    const qs = params.toString();
    const url = qs ? `${$page.url.pathname}?${qs}` : $page.url.pathname;
    goto(resolve(url as "/"), { replaceState: true, keepFocus: true, noScroll: true });
  }

  function handleTabChange(value: string) {
    activeTab = (value as TabId) ?? "startup";
    syncUrl();
    ensureTabLoaded(activeTab);
  }

  onMount(async () => {
    await auth.checkAuth();
    const urlTab = $page.url.searchParams.get("tab");
    if (urlTab === "playback" || urlTab === "retention") activeTab = urlTab;
    const urlAsset = $page.url.searchParams.get("asset");
    if (urlAsset) selectedAsset = urlAsset;
    await ensureTabLoaded(activeTab);
  });

  function rangeVars() {
    const range = resolveTimeRange(timeRange);
    return { timeRange: { start: range.start, end: range.end } };
  }

  // Fetch a tab's data the first time it's shown for the current range. The
  // Retention asset list (count + page over client_qoe_session_deltas FINAL) is the
  // most expensive query, so it never runs unless the Retention tab is opened.
  async function ensureTabLoaded(tab: TabId) {
    if (loadedTabs[tab]) return;
    const token = ++tabLoadToken;
    const epoch = rangeEpoch;
    tabLoading = true;
    try {
      if (tab === "startup") await loadStartup(epoch);
      else if (tab === "playback") await loadPlayback(epoch);
      else await loadRetentionTab(epoch);
      if (token !== tabLoadToken || epoch !== rangeEpoch) return;
      loadedTabs[tab] = true;
    } catch (error) {
      if (token !== tabLoadToken || epoch !== rangeEpoch) return;
      console.error("Failed to load player experience data:", error);
      toast.error("Failed to load player experience analytics.");
    } finally {
      if (token === tabLoadToken && epoch === rangeEpoch) tabLoading = false;
    }
  }

  async function loadStartup(epoch: number) {
    const timeVars = rangeVars();
    const seriesVars = { ...timeVars, interval: intervalForRange(timeRange) };
    const [summaryResult, seriesResult, clusterResult] = await Promise.all([
      bootSummaryStore.fetch({ variables: timeVars }).catch(() => null),
      bootSeriesStore.fetch({ variables: seriesVars }).catch(() => null),
      // Cluster-ops is operator-only; non-operators get an auth error — ignore it.
      clusterBootStore.fetch({ variables: timeVars }).catch(() => null),
    ]);
    if (epoch !== rangeEpoch) return;
    bootSummary = summaryResult?.data?.analytics?.health?.playerBootSummary ?? null;
    bootSeries = seriesResult?.data?.analytics?.health?.playerBootTimeSeries ?? [];
    bootClusterRows = clusterResult?.data?.analytics?.infra?.clusterBootOps ?? [];
  }

  async function loadPlayback(epoch: number) {
    const timeVars = rangeVars();
    const seriesVars = { ...timeVars, interval: intervalForRange(timeRange) };
    const [summaryResult, seriesResult, clusterResult] = await Promise.all([
      qoeSummaryStore.fetch({ variables: timeVars }).catch(() => null),
      qoeSeriesStore.fetch({ variables: seriesVars }).catch(() => null),
      clusterQoeStore.fetch({ variables: timeVars }).catch(() => null),
    ]);
    if (epoch !== rangeEpoch) return;
    qoeSummary = summaryResult?.data?.analytics?.health?.sessionQoeSummary ?? null;
    qoeSeries = seriesResult?.data?.analytics?.health?.sessionQoeTimeSeries ?? [];
    qoeClusterRows = clusterResult?.data?.analytics?.infra?.clusterQoeOps ?? [];
  }

  async function loadRetentionTab(epoch: number) {
    assetCursor = {};
    retention = null;
    await loadAssets(epoch);
    // Restore a deep-linked (?asset=) curve when landing on the Retention tab.
    if (epoch !== rangeEpoch) return;
    if (selectedAsset) await loadRetention(selectedAsset, epoch);
  }

  async function loadAssets(epoch = rangeEpoch) {
    const token = ++assetsLoadToken;
    assetsLoading = true;
    try {
      const range = resolveTimeRange(timeRange);
      const pageArg: { first?: number; after?: string; last?: number; before?: string } =
        assetCursor.before
          ? { last: ASSET_PAGE_SIZE, before: assetCursor.before }
          : { first: ASSET_PAGE_SIZE, after: assetCursor.after };
      const result = await assetsStore.fetch({
        variables: { page: pageArg, timeRange: { start: range.start, end: range.end } },
      });
      if (token !== assetsLoadToken || epoch !== rangeEpoch) return;
      assetsConn = result.data?.analytics?.health?.vodRetentionAssets ?? null;
    } catch (error) {
      if (token !== assetsLoadToken || epoch !== rangeEpoch) return;
      console.error("Failed to load retention assets:", error);
    } finally {
      if (token === assetsLoadToken && epoch === rangeEpoch) assetsLoading = false;
    }
  }

  async function loadRetention(hash: string, epoch = rangeEpoch) {
    if (!hash) return;
    const token = ++retentionLoadToken;
    retentionLoading = true;
    try {
      const range = resolveTimeRange(timeRange);
      const result = await retentionStore.fetch({
        variables: { artifactHash: hash, timeRange: { start: range.start, end: range.end } },
      });
      if (token !== retentionLoadToken || epoch !== rangeEpoch || selectedAsset !== hash) return;
      retention = result.data?.analytics?.health?.vodRetention ?? null;
    } catch (error) {
      if (token !== retentionLoadToken || epoch !== rangeEpoch) return;
      console.error("Failed to load retention:", error);
      toast.error("Failed to load VOD retention.");
    } finally {
      if (token === retentionLoadToken && epoch === rangeEpoch) retentionLoading = false;
    }
  }

  function selectAsset(hash: string) {
    selectedAsset = hash;
    syncUrl();
    loadRetention(hash);
  }

  function nextAssetPage() {
    if (!assetsConn?.pageInfo?.hasNextPage) return;
    assetCursor = { after: assetsConn.pageInfo.endCursor ?? undefined };
    loadAssets();
  }
  function prevAssetPage() {
    if (!assetsConn?.pageInfo?.hasPreviousPage) return;
    assetCursor = { before: assetsConn.pageInfo.startCursor ?? undefined };
    loadAssets();
  }

  function onRangeChange(value: string) {
    timeRange = value;
    rangeEpoch += 1;
    // A new range invalidates every tab; refetch only the visible one now and let
    // the others lazy-load when reopened.
    loadedTabs = { startup: false, playback: false, retention: false };
    ensureTabLoaded(activeTab);
  }

  // Average span breakdown for the boot waterfall table.
  let spanRows = $derived(
    bootSummary
      ? [
          { label: "Gateway resolve (GraphQL)", value: bootSummary.avgGatewayResolveMs },
          { label: "Mist hydrate (json_*.js)", value: bootSummary.avgMistHydrateMs },
          { label: "Player select", value: bootSummary.avgPlayerSelectMs },
          { label: "Connect", value: bootSummary.avgConnectMs },
          { label: "Prebuffer", value: bootSummary.avgPrebufferMs },
        ]
      : []
  );

  const tabTriggerClass =
    "gap-2 px-4 py-3 text-sm font-medium text-muted-foreground border-b-2 border-transparent rounded-none data-[state=active]:text-info data-[state=active]:border-info cursor-pointer hover:bg-muted/20 transition-colors";
</script>

<svelte:head>
  <title>Player Experience - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div
    class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 flex justify-between items-center"
  >
    <div class="flex items-center gap-3">
      <RocketIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Player Experience</h1>
        <p class="text-sm text-muted-foreground">
          Startup, playback quality, and VOD retention (diagnostic, sampled)
        </p>
      </div>
    </div>
    <Select value={timeRange} onValueChange={onRangeChange} type="single">
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

  <div class="flex-1 overflow-y-auto">
    <div class="page-transition">
      <Tabs value={activeTab} onValueChange={handleTabChange} class="w-full gap-0">
        <TabsList
          class="flex w-full rounded-none p-0 h-auto bg-[hsl(var(--tn-bg-dark)/0.5)] border-b border-[hsl(var(--tn-fg-gutter)/0.3)] justify-start items-center"
        >
          <TabsTrigger value="startup" class={tabTriggerClass}>
            <ZapIcon class="w-4 h-4" />
            Startup
          </TabsTrigger>
          <TabsTrigger value="playback" class={tabTriggerClass}>
            <ActivityIcon class="w-4 h-4" />
            Playback
          </TabsTrigger>
          <TabsTrigger value="retention" class={tabTriggerClass}>
            <FilmIcon class="w-4 h-4" />
            Retention
          </TabsTrigger>
        </TabsList>

        <!-- Startup tab -->
        <TabsContent value="startup" class="mt-0">
          {#if tabLoading && activeTab === "startup"}
            <div class="flex items-center justify-center min-h-64">
              <div class="loading-spinner w-8 h-8"></div>
            </div>
          {:else}
            <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
              <div>
                <DashboardMetricCard
                  icon={ZapIcon}
                  iconColor="text-primary"
                  value={ms(bootSummary?.p50TtfMs)}
                  valueColor="text-primary"
                  label="TTF p50"
                />
              </div>
              <div>
                <DashboardMetricCard
                  icon={ZapIcon}
                  iconColor="text-warning"
                  value={ms(bootSummary?.p95TtfMs)}
                  valueColor="text-warning"
                  label="TTF p95"
                />
              </div>
              <div>
                <DashboardMetricCard
                  icon={ZapIcon}
                  iconColor="text-purple-500"
                  value={ms(bootSummary?.p99TtfMs)}
                  valueColor="text-purple-500"
                  label="TTF p99"
                />
              </div>
              <div>
                <DashboardMetricCard
                  icon={AlertTriangleIcon}
                  iconColor="text-destructive"
                  value={bootSummary ? `${bootSummary.errorCount}/${bootSummary.bootCount}` : "—"}
                  valueColor="text-destructive"
                  label="Errors / boots"
                />
              </div>
            </GridSeam>

            <div class="px-4 sm:px-6 lg:px-8 py-6 space-y-8">
              <section>
                <h2 class="text-sm font-semibold text-foreground mb-3">
                  Time-to-first-frame over time
                </h2>
                {#if bootSeries.length > 0}
                  <BootTtfTrendChart data={[...bootSeries]} />
                {:else}
                  <p class="text-xs text-muted-foreground">No boot samples in this time range.</p>
                {/if}
              </section>

              <section>
                <h2 class="text-sm font-semibold text-foreground mb-3">
                  Boot waterfall — average span breakdown
                </h2>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Span</TableHead>
                      <TableHead class="text-right">Avg</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {#each spanRows as row (row.label)}
                      <TableRow>
                        <TableCell>{row.label}</TableCell>
                        <TableCell class="text-right">{ms(row.value)}</TableCell>
                      </TableRow>
                    {/each}
                  </TableBody>
                </Table>
                <p class="text-xs text-muted-foreground mt-2">
                  Cache hit ratio on player-owned fetches: {pct(bootSummary?.cacheHitRatio, 0)}
                </p>
              </section>

              {#if bootClusterRows.length > 0}
                <section>
                  <div class="flex items-center gap-2 mb-3">
                    <h2 class="text-sm font-semibold text-foreground">Cluster operations</h2>
                    <Badge
                      variant="outline"
                      class="text-[10px] px-1.5 py-0 text-muted-foreground border-muted-foreground/30"
                    >
                      token-attributed
                    </Badge>
                  </div>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Cluster</TableHead>
                        <TableHead>Node</TableHead>
                        <TableHead>Protocol</TableHead>
                        <TableHead class="text-right">Boots</TableHead>
                        <TableHead class="text-right">Errors</TableHead>
                        <TableHead class="text-right">p95 TTF</TableHead>
                        <TableHead class="text-right">Cache hit</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {#each bootClusterRows as row (row.servingClusterId + ":" + row.nodeId + ":" + row.protocol)}
                        <TableRow>
                          <TableCell>{row.servingClusterId}</TableCell>
                          <TableCell>{row.nodeId}</TableCell>
                          <TableCell>{row.protocol}</TableCell>
                          <TableCell class="text-right">{row.bootCount}</TableCell>
                          <TableCell class="text-right">{row.errorCount}</TableCell>
                          <TableCell class="text-right">{ms(row.p95TtfMs)}</TableCell>
                          <TableCell class="text-right">{pct(row.cacheHitRatio, 0)}</TableCell>
                        </TableRow>
                      {/each}
                    </TableBody>
                  </Table>
                </section>
              {/if}
            </div>
          {/if}
        </TabsContent>

        <!-- Playback tab -->
        <TabsContent value="playback" class="mt-0">
          {#if tabLoading && activeTab === "playback"}
            <div class="flex items-center justify-center min-h-64">
              <div class="loading-spinner w-8 h-8"></div>
            </div>
          {:else}
            <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
              <div>
                <DashboardMetricCard
                  icon={PauseIcon}
                  iconColor="text-primary"
                  value={pct(qoeSummary?.rebufferingRatio)}
                  valueColor="text-primary"
                  label="Rebuffering ratio"
                  subtitle="time stalled ÷ time watched"
                />
              </div>
              <div>
                <DashboardMetricCard
                  icon={PauseIcon}
                  iconColor="text-warning"
                  value={num(qoeSummary?.rebuffersPerHour)}
                  valueColor="text-warning"
                  label="Rebuffers / hour"
                />
              </div>
              <div>
                <DashboardMetricCard
                  icon={GaugeIcon}
                  iconColor="text-cyan-500"
                  value={mbps(qoeSummary?.avgBitrateBps)}
                  valueColor="text-cyan-500"
                  label="Avg bitrate"
                />
              </div>
              <div>
                <DashboardMetricCard
                  icon={AlertTriangleIcon}
                  iconColor="text-destructive"
                  value={pct(qoeSummary?.ebvsRate)}
                  valueColor="text-destructive"
                  label="Exit before start"
                />
              </div>
            </GridSeam>

            <div class="px-4 sm:px-6 lg:px-8 py-6 space-y-8">
              <section>
                <h2 class="text-sm font-semibold text-foreground mb-3">
                  Quality of experience over time
                </h2>
                {#if qoeSeries.length > 0}
                  <SessionQoeTrendChart data={[...qoeSeries]} />
                {:else}
                  <p class="text-xs text-muted-foreground">
                    No playback sessions in this time range.
                  </p>
                {/if}
              </section>

              <section>
                <h2 class="text-sm font-semibold text-foreground mb-3">Quality of experience</h2>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Metric</TableHead>
                      <TableHead class="text-right">Value</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    <TableRow>
                      <TableCell>Sessions</TableCell>
                      <TableCell class="text-right">{qoeSummary?.sessionCount ?? "—"}</TableCell>
                    </TableRow>
                    <TableRow>
                      <TableCell>Watch time (hours)</TableCell>
                      <TableCell class="text-right">{num(qoeSummary?.playedHours)}</TableCell>
                    </TableRow>
                    <TableRow>
                      <TableCell>Avg rebuffer duration</TableCell>
                      <TableCell class="text-right"
                        >{num(qoeSummary?.avgRebufferMs, 0)} ms</TableCell
                      >
                    </TableRow>
                    <TableRow>
                      <TableCell>Frame drop ratio</TableCell>
                      <TableCell class="text-right">{pct(qoeSummary?.frameDropRatio, 3)}</TableCell>
                    </TableRow>
                    <TableRow>
                      <TableCell>Mid-stream failure rate</TableCell>
                      <TableCell class="text-right"
                        >{pct(qoeSummary?.playbackFailureRate)}</TableCell
                      >
                    </TableRow>
                    <TableRow>
                      <TableCell>ABR switches / hour</TableCell>
                      <TableCell class="text-right">{num(qoeSummary?.abrSwitchesPerHour)}</TableCell
                      >
                    </TableRow>
                    <TableRow>
                      <TableCell>Avg live-edge latency</TableCell>
                      <TableCell class="text-right"
                        >{num(qoeSummary?.avgLiveEdgeLatencyMs, 0)} ms</TableCell
                      >
                    </TableRow>
                  </TableBody>
                </Table>
              </section>

              {#if qoeClusterRows.length > 0}
                <section>
                  <div class="flex items-center gap-2 mb-3">
                    <h2 class="text-sm font-semibold text-foreground">Cluster operations</h2>
                    <Badge
                      variant="outline"
                      class="text-[10px] px-1.5 py-0 text-muted-foreground border-muted-foreground/30"
                    >
                      token-attributed
                    </Badge>
                  </div>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Cluster</TableHead>
                        <TableHead>Node</TableHead>
                        <TableHead>Protocol</TableHead>
                        <TableHead class="text-right">Sessions</TableHead>
                        <TableHead class="text-right">Rebuffer ratio</TableHead>
                        <TableHead class="text-right">Frame drop</TableHead>
                        <TableHead class="text-right">Avg bitrate</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {#each qoeClusterRows as row (row.servingClusterId + ":" + row.nodeId + ":" + row.protocol)}
                        <TableRow>
                          <TableCell>{row.servingClusterId}</TableCell>
                          <TableCell>{row.nodeId}</TableCell>
                          <TableCell>{row.protocol}</TableCell>
                          <TableCell class="text-right">{row.sessionCount}</TableCell>
                          <TableCell class="text-right">{pct(row.rebufferingRatio)}</TableCell>
                          <TableCell class="text-right">{pct(row.frameDropRatio, 3)}</TableCell>
                          <TableCell class="text-right">{mbps(row.avgBitrateBps)}</TableCell>
                        </TableRow>
                      {/each}
                    </TableBody>
                  </Table>
                </section>
              {/if}
            </div>
          {/if}
        </TabsContent>

        <!-- Retention tab -->
        <TabsContent value="retention" class="mt-0">
          {#if tabLoading && activeTab === "retention"}
            <div class="flex items-center justify-center min-h-64">
              <div class="loading-spinner w-8 h-8"></div>
            </div>
          {:else}
            <div class="px-4 sm:px-6 lg:px-8 py-6 grid grid-cols-1 lg:grid-cols-2 gap-8">
              <section>
                <div class="flex items-center gap-2 mb-3">
                  <FilmIcon class="w-4 h-4 text-muted-foreground" />
                  <h2 class="text-sm font-semibold text-foreground">
                    VOD assets with retention data
                  </h2>
                </div>
                <VodRetentionAssetPicker
                  assets={[...assets]}
                  selectedHash={selectedAsset}
                  loading={assetsLoading}
                  totalCount={assetsConn?.totalCount ?? 0}
                  hasNextPage={assetsConn?.pageInfo?.hasNextPage ?? false}
                  hasPreviousPage={canPageRetentionAssetsBackward}
                  onSelect={selectAsset}
                  onNext={nextAssetPage}
                  onPrev={prevAssetPage}
                />
              </section>

              <section>
                <h2 class="text-sm font-semibold text-foreground mb-3">Audience retention</h2>
                {#if retentionLoading}
                  <div class="flex items-center justify-center py-8">
                    <div class="loading-spinner w-6 h-6"></div>
                  </div>
                {:else if retention && selectedAsset}
                  <VodRetentionChart
                    points={[...retention.points]}
                    totalSessions={retention.totalSessions}
                    bucketWidthS={retention.bucketWidthS}
                    assetDurationS={retention.assetDurationS}
                  />
                {:else}
                  <p class="text-xs text-muted-foreground">
                    Select an asset to view its watch-density and audience-retention curves.
                  </p>
                {/if}
              </section>
            </div>
          {/if}
        </TabsContent>
      </Tabs>
    </div>
  </div>
</div>
