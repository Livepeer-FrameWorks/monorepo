<script lang="ts">
  import { onMount } from "svelte";
  import { GetPlayerBootSummaryStore, GetClusterBootOpsStore } from "$houdini";
  import { auth } from "$lib/stores/auth";
  import { toast } from "$lib/stores/toast.js";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Badge } from "$lib/components/ui/badge";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
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
  const clusterOpsStore = new GetClusterBootOpsStore();

  let timeRange = $state("24h");
  let currentRange = $derived(resolveTimeRange(timeRange));
  const timeRangeOptions = TIME_RANGE_OPTIONS;
  let loading = $state(true);

  let summary = $derived($bootSummaryStore.data?.analytics?.health?.playerBootSummary ?? null);
  let clusterRows = $derived($clusterOpsStore.data?.analytics?.infra?.clusterBootOps ?? []);

  const RocketIcon = getIconComponent("Rocket");
  const CalendarIcon = getIconComponent("Calendar");
  const ZapIcon = getIconComponent("Zap");
  const AlertTriangleIcon = getIconComponent("AlertTriangle");

  function ms(value: number | undefined | null): string {
    if (value === undefined || value === null) return "—";
    return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${Math.round(value)}ms`;
  }

  function pct(value: number | undefined | null): string {
    if (value === undefined || value === null) return "—";
    return `${(value * 100).toFixed(0)}%`;
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
        bootSummaryStore.fetch({ variables }).catch(() => null),
        // Cluster-ops is operator-only; ignore authorization errors for non-operators.
        clusterOpsStore.fetch({ variables }).catch(() => null),
      ]);
    } catch (error) {
      console.error("Failed to load player experience data:", error);
      toast.error("Failed to load player experience analytics.");
    } finally {
      loading = false;
    }
  }

  // Span breakdown rows for the waterfall table.
  let spanRows = $derived(
    summary
      ? [
          { label: "Gateway resolve (GraphQL)", value: summary.avgGatewayResolveMs },
          { label: "Mist hydrate (json_*.js)", value: summary.avgMistHydrateMs },
          { label: "Player select", value: summary.avgPlayerSelectMs },
          { label: "Connect", value: summary.avgConnectMs },
          { label: "Prebuffer", value: summary.avgPrebufferMs },
        ]
      : []
  );
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
          Startup time-to-first-frame and boot waterfall (diagnostic, sampled)
        </p>
      </div>
    </div>
    <Select
      value={timeRange}
      onValueChange={(value) => {
        timeRange = value;
        loadData();
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
              icon={ZapIcon}
              iconColor="text-primary"
              value={ms(summary?.p50TtfMs)}
              valueColor="text-primary"
              label="TTF p50"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={ZapIcon}
              iconColor="text-warning"
              value={ms(summary?.p95TtfMs)}
              valueColor="text-warning"
              label="TTF p95"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={ZapIcon}
              iconColor="text-purple-500"
              value={ms(summary?.p99TtfMs)}
              valueColor="text-purple-500"
              label="TTF p99"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={AlertTriangleIcon}
              iconColor="text-destructive"
              value={summary ? `${summary.errorCount}/${summary.bootCount}` : "—"}
              valueColor="text-destructive"
              label="Errors / boots"
            />
          </div>
        </GridSeam>

        <div class="px-4 sm:px-6 lg:px-8 py-6 space-y-8">
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
              Cache hit ratio on player-owned fetches: {pct(summary?.cacheHitRatio)}
            </p>
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
                    <TableHead class="text-right">Boots</TableHead>
                    <TableHead class="text-right">Errors</TableHead>
                    <TableHead class="text-right">p95 TTF</TableHead>
                    <TableHead class="text-right">Cache hit</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {#each clusterRows as row (row.servingClusterId + ":" + row.nodeId + ":" + row.protocol)}
                    <TableRow>
                      <TableCell>{row.servingClusterId}</TableCell>
                      <TableCell>{row.nodeId}</TableCell>
                      <TableCell>{row.protocol}</TableCell>
                      <TableCell class="text-right">{row.bootCount}</TableCell>
                      <TableCell class="text-right">{row.errorCount}</TableCell>
                      <TableCell class="text-right">{ms(row.p95TtfMs)}</TableCell>
                      <TableCell class="text-right">{pct(row.cacheHitRatio)}</TableCell>
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
