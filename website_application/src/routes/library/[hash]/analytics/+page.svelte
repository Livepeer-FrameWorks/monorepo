<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import {
    GetStorageArtifactsConnectionStore,
    GetVodRetentionStore,
    GetSessionQoeSummaryStore,
    GetPlayerBootSummaryStore,
    GetArtifactNodeCopiesStore,
    type GetVodRetention$result,
    type GetSessionQoeSummary$result,
    type GetPlayerBootSummary$result,
    type GetArtifactNodeCopies$result,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import VodRetentionChart from "$lib/components/charts/VodRetentionChart.svelte";
  import GeoView from "$lib/components/charts/GeoView.svelte";
  import { formatBytes } from "$lib/utils/formatters";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { getIconComponent } from "$lib/iconUtils";

  const artifactsStore = new GetStorageArtifactsConnectionStore();
  const retentionStore = new GetVodRetentionStore();
  const qoeStore = new GetSessionQoeSummaryStore();
  const bootStore = new GetPlayerBootSummaryStore();
  const placementStore = new GetArtifactNodeCopiesStore();

  type Retention = GetVodRetention$result["analytics"]["health"]["vodRetention"];
  type QoeSummary = GetSessionQoeSummary$result["analytics"]["health"]["sessionQoeSummary"];
  type BootSummary = GetPlayerBootSummary$result["analytics"]["health"]["playerBootSummary"];
  type Placement =
    GetArtifactNodeCopies$result["analytics"]["health"]["artifactNodeCopies"]["copies"][number];

  let hash = $derived(page.params.hash as string);
  let timeRange = $state("30d");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS;

  let loading = $state(true);
  let rangeEpoch = 0;
  let loadToken = 0;

  let retention = $state<Retention | null>(null);
  let qoe = $state<QoeSummary | null>(null);
  let boot = $state<BootSummary | null>(null);
  let placement = $state<Placement[]>([]);
  // True when the node-copy list was capped server-side: render the count as a lower bound
  // ("N+"), never as an exact total.
  let placementTruncated = $state(false);

  // Per-panel load failures so a genuine backend/network error renders as an error
  // note rather than being indistinguishable from a legitimately empty result. An
  // expected auth/permission denial stays silent (empty-state) — some viewers simply
  // can't see analytics.
  let artifactFailed = $state(false);
  // True when the artifact/lifecycle query couldn't be answered at all — including an expected auth
  // denial (errored, but not a hard `failed`). Distinct from `artifactFailed` so the durable panel
  // shows "unavailable" instead of the false-knowledge claim "No confirmed durable S3 copy".
  let artifactUnavailable = $state(false);
  let retentionFailed = $state(false);
  let experienceFailed = $state(false);
  let placementFailed = $state(false);
  // True when the node-copies query couldn't be answered at all — including an auth
  // denial. Distinct from `placementFailed` (non-auth failure) so the empty-state shows
  // "unavailable" rather than the false-knowledge claim that no node holds a copy.
  let placementUnavailable = $state(false);

  function isExpectedAuthError(err: unknown): boolean {
    const msg = String((err as { message?: string })?.message ?? err ?? "").toLowerCase();
    return (
      msg.includes("permission") ||
      msg.includes("unauthor") ||
      msg.includes("forbidden") ||
      msg.includes("not allowed")
    );
  }

  // Runs a Houdini fetch and reports whether it failed for a non-auth reason (`failed`)
  // and whether it errored at all, auth-denials included (`errored`). Surfaces both thrown
  // network errors and in-band GraphQL `errors`. `errored` lets a panel distinguish an
  // authoritative empty result from "we couldn't ask", so it never states false knowledge.
  async function fetchPanel<T>(
    p: Promise<T>
  ): Promise<{ result: T | null; failed: boolean; errored: boolean }> {
    try {
      const result = await p;
      const errs = (result as { errors?: unknown[] })?.errors;
      if (Array.isArray(errs) && errs.length > 0) {
        return { result, failed: errs.some((e) => !isExpectedAuthError(e)), errored: true };
      }
      return { result, failed: false, errored: false };
    } catch (err) {
      return { result: null, failed: !isExpectedAuthError(err), errored: true };
    }
  }

  // Nodes holding a local copy, with geo, for the copies map.
  let placementNodes = $derived(
    placement
      .filter((p) => p.latitude != null && p.longitude != null)
      .map((p) => ({
        id: p.nodeId,
        name: `${p.nodeName ?? p.nodeId} · ${roleLabel(p.role)}`,
        lat: p.latitude as number,
        lng: p.longitude as number,
      }))
  );
  function roleLabel(role: string): string {
    return role === "origin" ? "Origin" : "Cached";
  }

  let artifact = $derived(
    ($artifactsStore.data?.storageArtifactsConnection?.nodes ?? []).find((n) => n.hash === hash) ??
      null
  );
  // False when the lifecycle backend was unavailable: the flags are unknown, not false,
  // so the durable-storage panel must say "unavailable", not "not synced".
  let lifecycleAvailable = $derived(
    $artifactsStore.data?.storageArtifactsConnection?.lifecycleAvailable ?? true
  );

  // The one durable copy lives in object storage (S3), and only counts once the artifact's durable
  // sync lifecycle (isSynced) confirms it. Node copies (above) are transient and tracked separately.
  // "frozen" here is the derived S3-only state: sync confirmed AND no warm edge copy observed
  // (hasLocalCopy === false); a null placement overlay (unknown) is not claimed as S3-only.
  let durableStorage = $derived.by(() => {
    if (!artifact) return null;
    if (!artifact.isSynced) return null;
    return {
      cluster: artifact.storageClusterId ?? null,
      frozen: artifact.hasLocalCopy === false,
      sizeBytes: artifact.sizeBytes ?? null,
    };
  });

  // Durable and local availability are INDEPENDENT: the artifact/lifecycle load and the
  // placement query can each fail on their own. Render "—" for whichever is unknown so the
  // summary never contradicts the panels below (which already show "unavailable"). Only a
  // genuinely-loaded-but-empty state shows 0.
  let durableCountLabel = $derived(
    artifactFailed || artifactUnavailable || !lifecycleAvailable
      ? "— durable S3 location"
      : `${durableStorage ? 1 : 0} durable S3 location`
  );
  let localCountLabel = $derived(
    placementFailed || placementUnavailable
      ? "— local node copies"
      : placementTruncated
        ? `${placement.length}+ local node copies (truncated)`
        : `${placement.length} local node ${placement.length === 1 ? "copy" : "copies"}`
  );

  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const FilmIcon = getIconComponent("Film");
  const CalendarIcon = getIconComponent("Calendar");
  const UsersIcon = getIconComponent("Users");
  const ClockIcon = getIconComponent("Clock");
  const PauseIcon = getIconComponent("Pause");
  const ZapIcon = getIconComponent("Zap");
  const HardDriveIcon = getIconComponent("HardDrive");

  function kindLabel(kind: string): string {
    switch (kind) {
      case "VOD":
        return "VOD";
      case "DVR":
        return "Recording";
      case "CHAPTER":
        return "Chapter";
      case "CLIP":
        return "Clip";
      default:
        return kind;
    }
  }
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

  async function load() {
    const token = ++loadToken;
    const epoch = rangeEpoch;
    loading = true;
    const range = resolveTimeRange(timeRange);
    const tr = { start: range.start, end: range.end };
    try {
      const [artResult, retResult, qoeResult, bootResult, placementResult] = await Promise.all([
        fetchPanel(
          artifactsStore.fetch({ variables: { input: { artifactHash: hash, first: 1 } } })
        ),
        fetchPanel(retentionStore.fetch({ variables: { artifactHash: hash, timeRange: tr } })),
        fetchPanel(qoeStore.fetch({ variables: { artifactHash: hash, timeRange: tr } })),
        fetchPanel(bootStore.fetch({ variables: { artifactHash: hash, timeRange: tr } })),
        fetchPanel(placementStore.fetch({ variables: { artifactHash: hash } })),
      ]);
      if (token !== loadToken || epoch !== rangeEpoch) return;
      retention = retResult.result?.data?.analytics?.health?.vodRetention ?? null;
      qoe = qoeResult.result?.data?.analytics?.health?.sessionQoeSummary ?? null;
      boot = bootResult.result?.data?.analytics?.health?.playerBootSummary ?? null;
      const nodeCopies = placementResult.result?.data?.analytics?.health?.artifactNodeCopies;
      placement = nodeCopies?.copies ?? [];
      placementTruncated = nodeCopies?.truncated ?? false;

      artifactFailed = artResult.failed;
      artifactUnavailable = artResult.errored;
      retentionFailed = retResult.failed;
      experienceFailed = qoeResult.failed || bootResult.failed;
      placementFailed = placementResult.failed;
      placementUnavailable = placementResult.errored;

      if (artifactFailed || retentionFailed || experienceFailed || placementFailed) {
        toast.error("Some analytics panels failed to load.");
      }
    } catch (error) {
      if (token !== loadToken || epoch !== rangeEpoch) return;
      console.error("Failed to load asset analytics:", error);
      toast.error("Failed to load asset analytics.");
    } finally {
      if (token === loadToken && epoch === rangeEpoch) loading = false;
    }
  }

  function onRangeChange(value: string) {
    timeRange = value;
    rangeEpoch += 1;
    load();
  }

  onMount(load);
</script>

<svelte:head>
  <title>{artifact?.title ?? "Asset"} analytics - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div
    class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 flex justify-between items-center gap-4"
  >
    <div class="flex items-center gap-3 min-w-0">
      <Button
        variant="ghost"
        size="icon"
        onclick={() => goto(resolve(`/library/${hash}`))}
        title="Back to asset"
      >
        <ArrowLeftIcon class="w-4 h-4" />
      </Button>
      <FilmIcon class="w-5 h-5 text-primary shrink-0" />
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <h1 class="text-xl font-bold text-foreground truncate">
            {artifact?.title ?? "Asset"} analytics
          </h1>
          {#if artifact}
            <Badge variant="outline" class="text-[10px] shrink-0">{kindLabel(artifact.kind)}</Badge>
          {/if}
        </div>
        <p class="text-sm text-muted-foreground">Audience retention and player experience</p>
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
    {#if loading}
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    {:else}
      <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
        <div>
          <DashboardMetricCard
            icon={UsersIcon}
            iconColor="text-primary"
            value={retention?.totalSessions ?? qoe?.sessionCount ?? "—"}
            valueColor="text-primary"
            label="Sessions"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={ClockIcon}
            iconColor="text-cyan-500"
            value={num(qoe?.playedHours)}
            valueColor="text-cyan-500"
            label="Watch hours"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={PauseIcon}
            iconColor="text-warning"
            value={pct(qoe?.rebufferingRatio)}
            valueColor="text-warning"
            label="Rebuffering ratio"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={ZapIcon}
            iconColor="text-success"
            value={ms(boot?.p50TtfMs)}
            valueColor="text-success"
            label="TTF p50"
          />
        </div>
      </GridSeam>

      <div class="px-4 sm:px-6 lg:px-8 py-6 space-y-8">
        <!-- Audience retention -->
        <section>
          <div class="flex items-center gap-2 mb-3">
            <FilmIcon class="w-4 h-4 text-primary" />
            <h2 class="text-sm font-semibold text-foreground">Audience retention</h2>
          </div>
          {#if retention && retention.totalSessions > 0}
            <VodRetentionChart
              points={[...retention.points]}
              totalSessions={retention.totalSessions}
              bucketWidthS={retention.bucketWidthS}
              assetDurationS={retention.assetDurationS}
            />
          {:else if retentionFailed}
            <p class="text-xs text-destructive">Couldn't load audience retention. Try again.</p>
          {:else}
            <p class="text-xs text-muted-foreground">
              No audience-retention data for this asset in the selected range.
            </p>
          {/if}
        </section>

        <!-- Player experience -->
        <section>
          <div class="flex items-center gap-2 mb-3">
            <ZapIcon class="w-4 h-4 text-primary" />
            <h2 class="text-sm font-semibold text-foreground">Player experience</h2>
          </div>
          {#if qoe && qoe.sessionCount > 0}
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Metric</TableHead>
                  <TableHead class="text-right">Value</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow>
                  <TableCell>Avg bitrate</TableCell>
                  <TableCell class="text-right">{mbps(qoe.avgBitrateBps)}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>Rebuffers / hour</TableCell>
                  <TableCell class="text-right">{num(qoe.rebuffersPerHour)}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>Frame drop ratio</TableCell>
                  <TableCell class="text-right">{pct(qoe.frameDropRatio, 3)}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>Exit before start</TableCell>
                  <TableCell class="text-right">{pct(qoe.ebvsRate)}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>TTF p95</TableCell>
                  <TableCell class="text-right">{ms(boot?.p95TtfMs)}</TableCell>
                </TableRow>
              </TableBody>
            </Table>
          {:else if experienceFailed}
            <p class="text-xs text-destructive">Couldn't load player experience. Try again.</p>
          {:else}
            <p class="text-xs text-muted-foreground">
              No player-experience samples for this asset in the selected range.
            </p>
          {/if}
        </section>

        <!-- Storage & copies: the durable object-storage copy plus transient node copies -->
        <section>
          <div class="flex items-center gap-2 mb-3">
            <HardDriveIcon class="w-4 h-4 text-primary" />
            <h2 class="text-sm font-semibold text-foreground">Storage &amp; copies</h2>
            <span class="text-xs text-muted-foreground">
              {durableCountLabel} · {localCountLabel}
            </span>
          </div>

          <!-- Durable storage: the one authoritative copy, in object storage -->
          <div class="mb-4 rounded-md border border-border/40 p-3">
            <div class="text-xs font-medium text-muted-foreground mb-1">Durable storage</div>
            {#if artifactFailed}
              <p class="text-xs text-destructive">Couldn't load storage state. Try again.</p>
            {:else if artifactUnavailable}
              <!-- The asset query errored (e.g. permission denied) — durable state is unknown, so
                   don't assert "no durable copy". -->
              <p class="text-xs text-muted-foreground">Durable storage state is unavailable.</p>
            {:else if !lifecycleAvailable}
              <p class="text-xs text-warning">
                Storage lifecycle is temporarily unavailable — durable state unknown.
              </p>
            {:else if durableStorage}
              <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
                <span class="font-medium text-foreground">
                  {durableStorage.frozen ? "Frozen · S3 (read-through)" : "Synced · S3"}
                </span>
                <span class="text-muted-foreground">
                  Cluster: {durableStorage.cluster ?? "—"}
                </span>
                {#if durableStorage.sizeBytes}
                  <span class="font-mono text-muted-foreground"
                    >{formatBytes(durableStorage.sizeBytes)}</span
                  >
                {/if}
              </div>
            {:else}
              <p class="text-xs text-muted-foreground">No confirmed durable S3 copy.</p>
            {/if}
          </div>

          <!-- Node copies: transient local copies across nodes -->
          <div class="text-xs font-medium text-muted-foreground mb-2">Node copies</div>
          {#if placement.length > 0}
            {#if placementNodes.length > 0}
              <div class="slab-body--flush mb-4" style="height: 360px;">
                <GeoView nodes={placementNodes} height={360} variant="placement" />
              </div>
            {/if}
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Node</TableHead>
                  <TableHead>Cluster</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Complete</TableHead>
                  <!-- Node-copy size is the value at the last emitted transition, not a
                       live figure — a still-growing DVR copy reads its last-reported size. -->
                  <TableHead class="text-right" title="Size at the last reported copy transition">
                    Size (last update)
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {#each placement as p (p.nodeId + ":" + p.role)}
                  <TableRow>
                    <TableCell class="font-mono text-xs">{p.nodeName ?? p.nodeId}</TableCell>
                    <TableCell class="text-muted-foreground">{p.clusterId ?? "—"}</TableCell>
                    <TableCell>{roleLabel(p.role)}</TableCell>
                    <TableCell class="text-muted-foreground"
                      >{p.isComplete ? "Yes" : "No"}</TableCell
                    >
                    <TableCell class="text-right font-mono">
                      {p.sizeBytes ? formatBytes(p.sizeBytes) : "—"}
                    </TableCell>
                  </TableRow>
                {/each}
              </TableBody>
            </Table>
            {#if placementTruncated}
              <p class="text-xs text-muted-foreground mt-2">
                Showing the first {placement.length} node copies — the list was truncated and is not exhaustive.
              </p>
            {/if}
          {:else if placementFailed}
            <p class="text-xs text-destructive">Couldn't load node copies. Try again.</p>
          {:else if placementUnavailable}
            <!-- The query errored (e.g. permission denied) — we don't know the copy state,
                 so don't assert that no node holds a copy. -->
            <p class="text-xs text-muted-foreground">Node-copy information is unavailable.</p>
          {:else}
            <p class="text-xs text-muted-foreground">No node currently holds a local copy.</p>
          {/if}
        </section>
      </div>
    {/if}
  </div>
</div>
