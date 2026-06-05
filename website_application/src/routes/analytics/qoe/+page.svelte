<script lang="ts">
  import { onMount } from "svelte";
  import { GetSessionQoeSummaryStore, GetClusterQoeOpsStore, GetVodRetentionStore } from "$houdini";
  import { auth } from "$lib/stores/auth";
  import { toast } from "$lib/stores/toast.js";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Badge } from "$lib/components/ui/badge";
  import { Input } from "$lib/components/ui/input";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import VodRetentionChart from "$lib/components/charts/VodRetentionChart.svelte";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { getIconComponent } from "$lib/iconUtils";

  const qoeStore = new GetSessionQoeSummaryStore();
  const clusterStore = new GetClusterQoeOpsStore();
  const retentionStore = new GetVodRetentionStore();

  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS;
  let loading = $state(true);

  let artifactHash = $state("");
  let retentionLoading = $state(false);

  let summary = $derived($qoeStore.data?.analytics?.health?.sessionQoeSummary ?? null);
  let clusterRows = $derived($clusterStore.data?.analytics?.infra?.clusterQoeOps ?? []);
  let retention = $derived($retentionStore.data?.analytics?.health?.vodRetention ?? null);

  const ActivityIcon = getIconComponent("Activity");
  const CalendarIcon = getIconComponent("Calendar");
  const PauseIcon = getIconComponent("Pause");
  const FilmIcon = getIconComponent("Film");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");
  const GaugeIcon = getIconComponent("Gauge");

  // Rebuffering ratio (and friends) are 0..1 fractions; show as percentages.
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

  onMount(async () => {
    await auth.checkAuth();
    await loadData();
  });

  async function loadData() {
    loading = true;
    try {
      const range = resolveTimeRange(timeRange);
      const variables = { timeRange: { start: range.start, end: range.end } };
      await Promise.all([
        qoeStore.fetch({ variables }).catch(() => null),
        // Cluster-ops is operator-only; non-operators get an auth error — ignore it.
        clusterStore.fetch({ variables }).catch(() => null),
      ]);
    } catch (error) {
      console.error("Failed to load QoE data:", error);
      toast.error("Failed to load playback QoE analytics.");
    } finally {
      loading = false;
    }
  }

  async function loadRetention() {
    const hash = artifactHash.trim();
    if (!hash) return;
    retentionLoading = true;
    try {
      const range = resolveTimeRange(timeRange);
      await retentionStore.fetch({
        variables: { artifactHash: hash, timeRange: { start: range.start, end: range.end } },
      });
    } catch (error) {
      console.error("Failed to load retention:", error);
      toast.error("Failed to load VOD retention.");
    } finally {
      retentionLoading = false;
    }
  }
</script>

<svelte:head>
  <title>Playback QoE - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div
    class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 flex justify-between items-center"
  >
    <div class="flex items-center gap-3">
      <ActivityIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Playback QoE</h1>
        <p class="text-sm text-muted-foreground">
          Viewer-experienced quality — rebuffering, frame drops, bitrate (diagnostic)
        </p>
      </div>
    </div>
    <Select
      value={timeRange}
      onValueChange={(value) => {
        timeRange = value;
        loadData();
        if (artifactHash.trim()) loadRetention();
      }}
      type="single"
    >
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
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="flex items-center justify-center min-h-64">
          <div class="loading-spinner w-8 h-8"></div>
        </div>
      </div>
    {:else}
      <div class="page-transition">
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
          <div>
            <DashboardMetricCard
              icon={PauseIcon}
              iconColor="text-primary"
              value={pct(summary?.rebufferingRatio)}
              valueColor="text-primary"
              label="Rebuffering ratio"
              subtitle="time stalled ÷ time watched"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={PauseIcon}
              iconColor="text-warning"
              value={num(summary?.rebuffersPerHour)}
              valueColor="text-warning"
              label="Rebuffers / hour"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={GaugeIcon}
              iconColor="text-cyan-500"
              value={mbps(summary?.avgBitrateBps)}
              valueColor="text-cyan-500"
              label="Avg bitrate"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={AlertTriangleIcon}
              iconColor="text-destructive"
              value={pct(summary?.ebvsRate)}
              valueColor="text-destructive"
              label="Exit before start"
            />
          </div>
        </GridSeam>

        <div class="px-4 sm:px-6 lg:px-8 py-6 space-y-8">
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
                  <TableCell class="text-right">{summary?.sessionCount ?? "—"}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>Watch time (hours)</TableCell>
                  <TableCell class="text-right">{num(summary?.playedHours)}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>Avg rebuffer duration</TableCell>
                  <TableCell class="text-right">{num(summary?.avgRebufferMs, 0)} ms</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>Frame drop ratio</TableCell>
                  <TableCell class="text-right">{pct(summary?.frameDropRatio, 3)}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>Mid-stream failure rate</TableCell>
                  <TableCell class="text-right">{pct(summary?.playbackFailureRate)}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>ABR switches / hour</TableCell>
                  <TableCell class="text-right">{num(summary?.abrSwitchesPerHour)}</TableCell>
                </TableRow>
                <TableRow>
                  <TableCell>Avg live-edge latency</TableCell>
                  <TableCell class="text-right"
                    >{num(summary?.avgLiveEdgeLatencyMs, 0)} ms</TableCell
                  >
                </TableRow>
              </TableBody>
            </Table>
          </section>

          <section>
            <div class="flex items-center gap-2 mb-3">
              <FilmIcon class="w-4 h-4 text-muted-foreground" />
              <h2 class="text-sm font-semibold text-foreground">VOD retention</h2>
            </div>
            <div class="flex gap-2 mb-4 max-w-md">
              <Input
                placeholder="Artifact hash (VOD/clip)"
                bind:value={artifactHash}
                onkeydown={(e: KeyboardEvent) => {
                  if (e.key === "Enter") loadRetention();
                }}
              />
              <Button variant="outline" onclick={loadRetention} disabled={retentionLoading}>
                {retentionLoading ? "Loading…" : "Load"}
              </Button>
            </div>
            {#if retention}
              <VodRetentionChart
                points={retention.points}
                totalSessions={retention.totalSessions}
                bucketWidthS={retention.bucketWidthS}
                assetDurationS={retention.assetDurationS}
              />
            {:else}
              <p class="text-xs text-muted-foreground">
                Enter an artifact hash to view its watch-density and audience-retention curves.
              </p>
            {/if}
          </section>

          {#if clusterRows.length > 0}
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
                  {#each clusterRows as row (row.servingClusterId + ":" + row.nodeId + ":" + row.protocol)}
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
      </div>
    {/if}
  </div>
</div>
