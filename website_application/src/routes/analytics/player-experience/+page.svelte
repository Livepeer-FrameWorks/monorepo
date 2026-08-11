<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { get } from "svelte/store";
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { SvelteURLSearchParams } from "svelte/reactivity";
  import {
    fragment,
    GetPlayerBootSummaryStore,
    GetClusterBootOpsStore,
    GetPlayerBootTimeSeriesStore,
    GetSessionQoeSummaryStore,
    GetClusterQoeOpsStore,
    GetSessionQoeTimeSeriesStore,
    GetStorageArtifactsConnectionStore,
    GetStreamsConnectionStore,
    StreamCoreFieldsStore,
    type GetPlayerBootSummary$result,
    type GetPlayerBootTimeSeries$result,
    type GetClusterBootOps$result,
    type GetSessionQoeSummary$result,
    type GetSessionQoeTimeSeries$result,
    type GetClusterQoeOps$result,
    type GetStorageArtifactsConnection$result,
  } from "$houdini";
  import { auth } from "$lib/stores/auth";
  import { toast } from "$lib/stores/toast.js";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import TrendChart from "$lib/components/charts/TrendChart.svelte";
  import BootWaterfall from "$lib/components/charts/BootWaterfall.svelte";
  import { palette, type TrendSeries } from "$lib/components/charts/theme";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { getIconComponent } from "$lib/iconUtils";

  const bootSummaryStore = new GetPlayerBootSummaryStore();
  const bootSeriesStore = new GetPlayerBootTimeSeriesStore();
  const clusterBootStore = new GetClusterBootOpsStore();
  const qoeSummaryStore = new GetSessionQoeSummaryStore();
  const qoeSeriesStore = new GetSessionQoeTimeSeriesStore();
  const clusterQoeStore = new GetClusterQoeOpsStore();
  const assetsStore = new GetStorageArtifactsConnectionStore();
  const streamsStore = new GetStreamsConnectionStore();
  const streamCoreStore = new StreamCoreFieldsStore();

  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS;

  type ScopeKind = "all" | "stream" | "asset";
  type BootSummary = GetPlayerBootSummary$result["analytics"]["health"]["playerBootSummary"];
  type BootSeries = GetPlayerBootTimeSeries$result["analytics"]["health"]["playerBootTimeSeries"];
  type BootClusterRows = GetClusterBootOps$result["analytics"]["infra"]["clusterBootOps"];
  type QoeSummary = GetSessionQoeSummary$result["analytics"]["health"]["sessionQoeSummary"];
  type QoeSeries = GetSessionQoeTimeSeries$result["analytics"]["health"]["sessionQoeTimeSeries"];
  type QoeClusterRows = GetClusterQoeOps$result["analytics"]["infra"]["clusterQoeOps"];
  type AssetsConnection = GetStorageArtifactsConnection$result["storageArtifactsConnection"];

  // Scope: tenant-wide by default; drill into one stream (Relay ID) or one stored
  // asset (artifactHash, any kind). The boot/QoE queries accept streamId OR
  // artifactHash OR neither, so the same panel serves all three scopes.
  let scope = $state<ScopeKind>("all");
  let scopeStreamId = $state<string | null>(null);
  let scopeAssetHash = $state<string | null>(null);
  let streamsLoaded = $state(false);
  let assetsLoaded = $state(false);

  let loading = $state(true);
  let loadFailed = $state(false);
  let showClusterOps = $state(false);
  let rangeEpoch = 0;
  let loadToken = 0;

  let bootSummary = $state<BootSummary | null>(null);
  let bootSeries = $state<BootSeries>([]);
  let bootClusterRows = $state<BootClusterRows>([]);
  let qoeSummary = $state<QoeSummary | null>(null);
  let qoeSeries = $state<QoeSeries>([]);
  let qoeClusterRows = $state<QoeClusterRows>([]);
  let assetsConn = $state<AssetsConnection | null>(null);

  // Unmask stream core fields for the scope dropdown (id is the Relay global ID
  // the boot/QoE resolvers expect; name is for display). Captured locally (rather than
  // read from the shared store) so the search generation guard fully controls the list.
  let streamsConnLocal = $state<typeof $streamsStore.data | null>(null);
  let streamOptions = $derived(
    (streamsConnLocal?.streamsConnection?.edges ?? []).map((e) => {
      const core = get(fragment(e.node, streamCoreStore));
      return { id: core.id, name: core.name };
    })
  );
  let assetOptions = $derived(
    (assetsConn?.nodes ?? []).map((a) => ({
      hash: a.hash,
      label: a.title || `${a.hash.slice(0, 12)}…`,
    }))
  );
  let scopeLabel = $derived.by(() => {
    if (scope === "stream")
      return streamOptions.find((s) => s.id === scopeStreamId)?.name ?? "Select a stream";
    if (scope === "asset")
      return assetOptions.find((a) => a.hash === scopeAssetHash)?.label ?? "Select an asset";
    return "";
  });

  const qoeTrendSeries: TrendSeries[] = [
    {
      key: "rebufferingRatio",
      label: "Rebuffering ratio",
      color: palette.blue,
      axis: "y",
      filled: true,
      scale: 100,
      unit: "%",
      digits: 3,
    },
    {
      key: "frameDropRatio",
      label: "Frame drop ratio",
      color: palette.red,
      axis: "y",
      scale: 100,
      unit: "%",
      digits: 3,
    },
    {
      key: "avgBitrateBps",
      label: "Avg bitrate",
      color: palette.cyan,
      axis: "y1",
      scale: 1 / 1_000_000,
      unit: " Mbps",
      digits: 2,
    },
  ];

  const bootTtfSeries: TrendSeries[] = [
    { key: "p50TtfMs", label: "TTF p50", color: palette.green, filled: true, format: (v) => ms(v) },
    { key: "p95TtfMs", label: "TTF p95", color: palette.yellow, format: (v) => ms(v) },
    { key: "p99TtfMs", label: "TTF p99", color: palette.purple, format: (v) => ms(v) },
  ];

  const RocketIcon = getIconComponent("Rocket");
  const ActivityIcon = getIconComponent("Activity");
  const CalendarIcon = getIconComponent("Calendar");
  const ZapIcon = getIconComponent("Zap");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");
  const PauseIcon = getIconComponent("Pause");
  const GaugeIcon = getIconComponent("Gauge");
  const ChevronDownIcon = getIconComponent("ChevronDown");
  const ChevronRightIcon = getIconComponent("ChevronRight");

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
    if (scope !== "all") params.set("scope", scope);
    if (scope === "stream" && scopeStreamId) params.set("stream", scopeStreamId);
    if (scope === "asset" && scopeAssetHash) params.set("asset", scopeAssetHash);
    const qs = params.toString();
    const url = qs ? `${$page.url.pathname}?${qs}` : $page.url.pathname;
    goto(resolve(url as "/"), { replaceState: true, keepFocus: true, noScroll: true });
  }

  // Scope variables only for the boot/QoE stores that declare them. Cluster-ops
  // are cluster aggregates and take timeRange only.
  function scopeVars() {
    return {
      streamId: scope === "stream" ? (scopeStreamId ?? undefined) : undefined,
      artifactHash: scope === "asset" ? (scopeAssetHash ?? undefined) : undefined,
    };
  }

  function timeRangeInput() {
    const range = resolveTimeRange(timeRange);
    return { start: range.start, end: range.end };
  }

  function isExpectedAuthError(err: unknown): boolean {
    const msg = String((err as { message?: string })?.message ?? err ?? "").toLowerCase();
    return (
      msg.includes("permission") ||
      msg.includes("unauthor") ||
      msg.includes("forbidden") ||
      msg.includes("not allowed")
    );
  }

  // Runs a Houdini fetch and reports whether it failed for a non-auth reason, so a
  // genuine backend failure is distinguishable from a legitimately empty result.
  async function fetchPanel<T>(p: Promise<T>): Promise<{ result: T | null; failed: boolean }> {
    try {
      const result = await p;
      const errs = (result as { errors?: unknown[] })?.errors;
      if (Array.isArray(errs) && errs.length > 0) {
        return { result, failed: errs.some((e) => !isExpectedAuthError(e)) };
      }
      return { result, failed: false };
    } catch (err) {
      return { result: null, failed: !isExpectedAuthError(err) };
    }
  }

  async function loadData() {
    const token = ++loadToken;
    const epoch = rangeEpoch;
    loading = true;
    const tr = timeRangeInput();
    const scoped = { timeRange: tr, ...scopeVars() };
    const scopedSeries = { ...scoped, interval: intervalForRange(timeRange) };
    const timeOnly = { timeRange: tr };
    try {
      const [bs, bt, bc, qs, qt, qc] = await Promise.all([
        fetchPanel(bootSummaryStore.fetch({ variables: scoped })),
        fetchPanel(bootSeriesStore.fetch({ variables: scopedSeries })),
        // Cluster-ops is operator-only; non-operators get an auth error we suppress.
        fetchPanel(clusterBootStore.fetch({ variables: timeOnly })),
        fetchPanel(qoeSummaryStore.fetch({ variables: scoped })),
        fetchPanel(qoeSeriesStore.fetch({ variables: scopedSeries })),
        fetchPanel(clusterQoeStore.fetch({ variables: timeOnly })),
      ]);
      if (token !== loadToken || epoch !== rangeEpoch) return;
      bootSummary = bs.result?.data?.analytics?.health?.playerBootSummary ?? null;
      bootSeries = bt.result?.data?.analytics?.health?.playerBootTimeSeries ?? [];
      bootClusterRows = bc.result?.data?.analytics?.infra?.clusterBootOps ?? [];
      qoeSummary = qs.result?.data?.analytics?.health?.sessionQoeSummary ?? null;
      qoeSeries = qt.result?.data?.analytics?.health?.sessionQoeTimeSeries ?? [];
      qoeClusterRows = qc.result?.data?.analytics?.infra?.clusterQoeOps ?? [];

      // Cluster-ops (bc, qc) auth failures are expected for non-operators — only the
      // tenant-scoped health panels signify a real load failure.
      loadFailed = bs.failed || bt.failed || qs.failed || qt.failed;
      if (loadFailed) toast.error("Some player-experience panels failed to load.");
    } catch (error) {
      if (token !== loadToken || epoch !== rangeEpoch) return;
      console.error("Failed to load player experience data:", error);
      loadFailed = true;
      toast.error("Failed to load player experience analytics.");
    } finally {
      if (token === loadToken && epoch === rangeEpoch) loading = false;
    }
  }

  // Server-side search makes the pickers account-wide instead of only the first page.
  let streamSearch = $state("");
  let assetSearch = $state("");
  let streamSearchTimer: ReturnType<typeof setTimeout> | undefined;
  let assetSearchTimer: ReturnType<typeof setTimeout> | undefined;
  // Generation guards so a slow older search can't overwrite newer results.
  let streamSearchGen = 0;
  let assetSearchGen = 0;
  let streamSearchError = $state(false);
  let assetSearchError = $state(false);

  // Houdini can resolve a fetch with an in-band `errors` array instead of rejecting.
  function hasGraphQLErrors(result: unknown): boolean {
    const errs = (result as { errors?: unknown[] })?.errors;
    return Array.isArray(errs) && errs.length > 0;
  }

  async function fetchStreamOptions(search: string) {
    const gen = ++streamSearchGen;
    streamSearchError = false;
    try {
      const result = await streamsStore.fetch({
        variables: { first: 50, search: search || undefined },
      });
      if (gen !== streamSearchGen) return; // a newer search superseded this one
      if (hasGraphQLErrors(result)) {
        streamSearchError = true;
        return;
      }
      streamsConnLocal = result.data ?? null;
      streamsLoaded = true;
    } catch (error) {
      if (gen !== streamSearchGen) return;
      console.error("Failed to load streams for scope:", error);
      streamSearchError = true;
    }
  }

  async function fetchAssetOptions(search: string) {
    const gen = ++assetSearchGen;
    assetSearchError = false;
    try {
      // All stored assets (clip/VOD/DVR/chapter), not just retention-eligible VOD —
      // the boot/QoE queries accept any artifactHash.
      const result = await assetsStore.fetch({
        variables: { input: { first: 50, search: search || undefined } },
      });
      if (gen !== assetSearchGen) return; // a newer search superseded this one
      if (hasGraphQLErrors(result)) {
        assetSearchError = true;
        return;
      }
      assetsConn = result.data?.storageArtifactsConnection ?? null;
      assetsLoaded = true;
    } catch (error) {
      if (gen !== assetSearchGen) return;
      console.error("Failed to load assets for scope:", error);
      assetSearchError = true;
    }
  }

  async function ensureStreamsLoaded() {
    if (streamsLoaded) return;
    await fetchStreamOptions("");
  }

  async function ensureAssetsLoaded() {
    if (assetsLoaded) return;
    await fetchAssetOptions("");
  }

  function onStreamSearch(v: string) {
    streamSearch = v;
    clearTimeout(streamSearchTimer);
    streamSearchTimer = setTimeout(() => fetchStreamOptions(v.trim()), 250);
  }

  function onAssetSearch(v: string) {
    assetSearch = v;
    clearTimeout(assetSearchTimer);
    assetSearchTimer = setTimeout(() => fetchAssetOptions(v.trim()), 250);
  }

  onDestroy(() => {
    clearTimeout(streamSearchTimer);
    clearTimeout(assetSearchTimer);
  });

  async function onScopeChange(next: ScopeKind) {
    scope = next;
    if (next === "stream") await ensureStreamsLoaded();
    if (next === "asset") await ensureAssetsLoaded();
    syncUrl();
    // Reload only when the scope resolves to a concrete filter (or back to all).
    if (
      next === "all" ||
      (next === "stream" && scopeStreamId) ||
      (next === "asset" && scopeAssetHash)
    )
      loadData();
  }

  function onScopeStreamChange(id: string) {
    scopeStreamId = id;
    syncUrl();
    loadData();
  }
  function onScopeAssetChange(hash: string) {
    scopeAssetHash = hash;
    syncUrl();
    loadData();
  }

  function onRangeChange(value: string) {
    timeRange = value;
    rangeEpoch += 1;
    assetsLoaded = false; // asset list is range-scoped; refresh lazily when needed
    if (scope === "asset") ensureAssetsLoaded();
    loadData();
  }

  onMount(async () => {
    await auth.checkAuth();
    const urlScope = $page.url.searchParams.get("scope");
    if (urlScope === "stream" || urlScope === "asset") scope = urlScope;
    scopeStreamId = $page.url.searchParams.get("stream");
    scopeAssetHash = $page.url.searchParams.get("asset");
    if (scope === "stream") await ensureStreamsLoaded();
    if (scope === "asset") await ensureAssetsLoaded();
    await loadData();
  });

  // Average span breakdown for the boot waterfall.
  let bootStages = $derived(
    bootSummary
      ? [
          {
            label: "Gateway resolve",
            ms: bootSummary.avgGatewayResolveMs ?? 0,
            color: palette.blue,
            hint: "GraphQL",
          },
          {
            label: "Mist hydrate",
            ms: bootSummary.avgMistHydrateMs ?? 0,
            color: palette.cyan,
            hint: "json_*.js",
          },
          {
            label: "Player select",
            ms: bootSummary.avgPlayerSelectMs ?? 0,
            color: palette.green,
            hint: "protocol scoring",
          },
          {
            label: "Connect",
            ms: bootSummary.avgConnectMs ?? 0,
            color: palette.yellow,
            hint: "transport + first byte",
          },
          {
            label: "Prebuffer",
            ms: bootSummary.avgPrebufferMs ?? 0,
            color: palette.orange,
            hint: "fill to first frame",
          },
        ]
      : []
  );

  const scopeBtnClass =
    "px-3 py-1.5 text-xs font-medium rounded-none border-b-2 border-transparent cursor-pointer transition-colors hover:text-foreground";
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
          Startup and playback quality, measured in the viewer's browser (diagnostic, sampled)
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

  <!-- Scope selector: neat tenant-wide view by default, per-stream / per-asset on request -->
  <div
    class="px-4 sm:px-6 lg:px-8 py-2.5 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 flex flex-wrap items-center gap-3 bg-[hsl(var(--tn-bg-dark)/0.3)]"
  >
    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wide">Scope</span>
    <div class="flex items-center rounded-md bg-[hsl(var(--tn-bg-dark)/0.5)] p-0.5">
      {#each [{ id: "all", label: "All" }, { id: "stream", label: "By stream" }, { id: "asset", label: "By asset" }] as opt (opt.id)}
        <button
          type="button"
          class={scopeBtnClass}
          class:text-info={scope === opt.id}
          class:text-muted-foreground={scope !== opt.id}
          class:border-info={scope === opt.id}
          onclick={() => onScopeChange(opt.id as ScopeKind)}
        >
          {opt.label}
        </button>
      {/each}
    </div>

    {#if scope === "stream"}
      <div class="flex items-center gap-2">
        <input
          type="search"
          placeholder="Search streams…"
          class="h-9 w-[180px] rounded-md border border-border bg-background px-3 text-sm"
          value={streamSearch}
          oninput={(e) => onStreamSearch(e.currentTarget.value)}
        />
        <Select value={scopeStreamId ?? ""} onValueChange={onScopeStreamChange} type="single">
          <SelectTrigger class="min-w-[200px]">{scopeLabel}</SelectTrigger>
          <SelectContent>
            {#each streamOptions as s (s.id)}
              <SelectItem value={s.id}>{s.name}</SelectItem>
            {/each}
          </SelectContent>
        </Select>
        {#if streamSearchError}
          <span class="text-xs text-destructive">Search failed</span>
        {/if}
      </div>
    {:else if scope === "asset"}
      <div class="flex items-center gap-2">
        <input
          type="search"
          placeholder="Search assets…"
          class="h-9 w-[180px] rounded-md border border-border bg-background px-3 text-sm"
          value={assetSearch}
          oninput={(e) => onAssetSearch(e.currentTarget.value)}
        />
        <Select value={scopeAssetHash ?? ""} onValueChange={onScopeAssetChange} type="single">
          <SelectTrigger class="min-w-[220px]">{scopeLabel}</SelectTrigger>
          <SelectContent>
            {#each assetOptions as a (a.hash)}
              <SelectItem value={a.hash}>{a.label}</SelectItem>
            {/each}
          </SelectContent>
        </Select>
        {#if assetSearchError}
          <span class="text-xs text-destructive">Search failed</span>
        {/if}
      </div>
    {/if}
  </div>

  <div class="flex-1 overflow-y-auto">
    <div class="page-transition">
      {#if loading}
        <div class="flex items-center justify-center min-h-64">
          <div class="loading-spinner w-8 h-8"></div>
        </div>
      {:else if scope === "stream" && !scopeStreamId}
        <div class="flex items-center justify-center min-h-64 text-sm text-muted-foreground">
          Select a stream to see its player experience.
        </div>
      {:else if scope === "asset" && !scopeAssetHash}
        <div class="flex items-center justify-center min-h-64 text-sm text-muted-foreground">
          Select an asset to see its player experience.
        </div>
      {:else if loadFailed}
        <div class="flex flex-col items-center justify-center min-h-64 gap-3">
          <p class="text-sm text-destructive">Couldn't load player-experience analytics.</p>
          <Button variant="outline" size="sm" onclick={() => loadData()}>Retry</Button>
        </div>
      {:else}
        <!-- Startup metric cards -->
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

        <!-- Playback metric cards -->
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
          <!-- Startup -->
          <section>
            <div class="flex items-center gap-2 mb-3">
              <ZapIcon class="w-4 h-4 text-primary" />
              <h2 class="text-sm font-semibold text-foreground">Startup</h2>
            </div>
            <div class="space-y-8">
              <div>
                <h3 class="text-xs font-medium text-muted-foreground mb-2">
                  Time-to-first-frame over time
                </h3>
                {#if bootSeries.length > 0}
                  <TrendChart
                    data={[...bootSeries]}
                    series={bootTtfSeries}
                    axes={{ y: { title: "Time to first frame", tickFormat: ms } }}
                    sampleKey="bootCount"
                    sampleNoun="boot"
                    maxTicks={8}
                  />
                {:else}
                  <p class="text-xs text-muted-foreground">No boot samples in this time range.</p>
                {/if}
              </div>
              <div>
                <h3 class="text-xs font-medium text-muted-foreground mb-2">
                  Boot waterfall — average span breakdown
                </h3>
                <BootWaterfall stages={bootStages} cacheHitRatio={bootSummary?.cacheHitRatio} />
              </div>
            </div>
          </section>

          <!-- Playback -->
          <section>
            <div class="flex items-center gap-2 mb-3">
              <ActivityIcon class="w-4 h-4 text-primary" />
              <h2 class="text-sm font-semibold text-foreground">Playback quality</h2>
            </div>
            <div class="space-y-8">
              <div>
                <h3 class="text-xs font-medium text-muted-foreground mb-2">
                  Quality of experience over time
                </h3>
                {#if qoeSeries.length > 0}
                  <TrendChart
                    data={[...qoeSeries]}
                    series={qoeTrendSeries}
                    leftTitle="Rebuffer / frame-drop %"
                    rightTitle="Bitrate (Mbps)"
                    sampleKey="sessionCount"
                    sampleNoun="session"
                  />
                {:else}
                  <p class="text-xs text-muted-foreground">
                    No playback sessions in this time range.
                  </p>
                {/if}
              </div>
              <div>
                <h3 class="text-xs font-medium text-muted-foreground mb-2">Session metrics</h3>
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
              </div>
            </div>
          </section>

          <!-- Cluster operations (operator detail, collapsed by default) -->
          {#if bootClusterRows.length > 0 || qoeClusterRows.length > 0}
            <section>
              <button
                type="button"
                class="flex items-center gap-2 text-sm font-semibold text-foreground mb-3 cursor-pointer"
                onclick={() => (showClusterOps = !showClusterOps)}
              >
                {#if showClusterOps}<ChevronDownIcon class="w-4 h-4" />{:else}<ChevronRightIcon
                    class="w-4 h-4"
                  />{/if}
                Cluster operations
                <Badge
                  variant="outline"
                  class="text-[10px] px-1.5 py-0 text-muted-foreground border-muted-foreground/30"
                >
                  token-attributed
                </Badge>
              </button>

              {#if showClusterOps}
                <div class="space-y-8">
                  {#if bootClusterRows.length > 0}
                    <div>
                      <h3 class="text-xs font-medium text-muted-foreground mb-2">
                        Startup by node
                      </h3>
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
                    </div>
                  {/if}

                  {#if qoeClusterRows.length > 0}
                    <div>
                      <h3 class="text-xs font-medium text-muted-foreground mb-2">
                        Playback by node
                      </h3>
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
                    </div>
                  {/if}
                </div>
              {/if}
            </section>
          {/if}
        </div>
      {/if}
    </div>
  </div>
</div>
