<script lang="ts">
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import {
    GetStorageUsageStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import StorageBreakdownChart from "$lib/components/charts/StorageBreakdownChart.svelte";
  import StorageTrendChart from "$lib/components/charts/StorageTrendChart.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Button } from "$lib/components/ui/button";
  import { getIconComponent } from "$lib/iconUtils";
  import { formatBytes } from "$lib/utils/formatters.js";
  import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
  } from "$lib/components/ui/select";

  // Houdini stores
  const storageUsageStore = new GetStorageUsageStore();

  // Type for storage usage edges
  type StorageUsageEdge = NonNullable<NonNullable<NonNullable<typeof $storageUsageStore.data>["storageUsageConnection"]>["edges"]>[0];

  // Derived state
  let loading = $derived($storageUsageStore.fetching);
  let usageRecords = $derived($storageUsageStore.data?.storageUsageConnection?.edges?.map((e: StorageUsageEdge) => e.node) ?? []);

  // Time range selection
  let timeRange = $state("7d"); // 24h, 7d, 30d

  // Computed latest snapshot for stats and breakdown
  let latestSnapshot = $derived.by(() => {
    if (usageRecords.length === 0) return null;
    // Sort by timestamp desc
    const sorted = [...usageRecords].sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime());
    return sorted[0];
  });

  // Computed data for trend chart
  let trendData = $derived.by(() => {
    return usageRecords.map(r => ({
      timestamp: r.timestamp,
      totalBytes: r.totalBytes,
      frozenBytes: (r.frozenDvrBytes || 0) + (r.frozenClipBytes || 0) + (r.frozenVodBytes || 0),
    }));
  });

  // Computed data for breakdown chart
  let breakdownData = $derived.by(() => {
    if (!latestSnapshot) return null;
    return {
      dvrBytes: latestSnapshot.dvrBytes,
      clipBytes: latestSnapshot.clipBytes,
      vodBytes: latestSnapshot.vodBytes,
      totalBytes: latestSnapshot.totalBytes,
    };
  });

  // Aggregated stats
  let totalStorage = $derived(latestSnapshot?.totalBytes ?? 0);
  let totalFrozen = $derived((latestSnapshot?.frozenDvrBytes ?? 0) + (latestSnapshot?.frozenClipBytes ?? 0) + (latestSnapshot?.frozenVodBytes ?? 0));
  let totalHot = $derived(totalStorage - totalFrozen);
  let totalFiles = $derived(latestSnapshot?.fileCount ?? 0);

  // Icons
  const DatabaseIcon = getIconComponent("Database");
  const HardDriveIcon = getIconComponent("HardDrive");
  const SnowflakeIcon = getIconComponent("Snowflake");
  const FileIcon = getIconComponent("File");
  const CalendarIcon = getIconComponent("Calendar");

  async function loadData() {
    try {
      const now = new Date();
      let start = new Date();

      switch (timeRange) {
        case "24h":
          start.setDate(now.getDate() - 1);
          break;
        case "7d":
          start.setDate(now.getDate() - 7);
          break;
        case "30d":
          start.setDate(now.getDate() - 30);
          break;
      }

      const range = {
        start: start.toISOString(),
        end: now.toISOString(),
      };

      await storageUsageStore.fetch({ variables: { timeRange: range } });
    } catch (error) {
      console.error("Failed to load storage data:", error);
      toast.error("Failed to load storage analytics.");
    }
  }

  onMount(() => {
    loadData();
  });

  function handleTimeRangeChange(value: string) {
    timeRange = value;
    loadData();
  }
</script>

<svelte:head>
  <title>Storage Analytics - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 bg-background">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-3">
        <DatabaseIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Storage Analytics</h1>
          <p class="text-sm text-muted-foreground">
            Monitor storage usage, cold archive status, and lifecycle events
          </p>
        </div>
      </div>
      
      <div class="flex items-center gap-2">
        <Select bind:value={timeRange} onValueChange={handleTimeRangeChange} type="single">
          <SelectTrigger class="w-auto min-w-[140px]">
            <CalendarIcon class="w-4 h-4 mr-2 text-muted-foreground" />
            {timeRange === '24h' ? 'Last 24 Hours' : timeRange === '7d' ? 'Last 7 Days' : 'Last 30 Days'}
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="24h">Last 24 Hours</SelectItem>
            <SelectItem value="7d">Last 7 Days</SelectItem>
            <SelectItem value="30d">Last 30 Days</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto bg-background/50">
    {#if loading && !latestSnapshot}
      <!-- Simple loading state -->
      <div class="p-8 flex justify-center">
        <div class="w-8 h-8 border-4 border-primary border-t-transparent rounded-full animate-spin"></div>
      </div>
    {:else}
      <div class="page-transition">
        <!-- Stats Bar -->
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
          <div>
            <DashboardMetricCard
              icon={DatabaseIcon}
              iconColor="text-primary"
              value={formatBytes(totalStorage)}
              valueColor="text-primary"
              label="Total Storage Used"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={HardDriveIcon}
              iconColor="text-success"
              value={formatBytes(totalHot)}
              valueColor="text-success"
              label="Hot (Local) Storage"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={SnowflakeIcon}
              iconColor="text-blue-400"
              value={formatBytes(totalFrozen)}
              valueColor="text-blue-400"
              label="Frozen (S3) Storage"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={FileIcon}
              iconColor="text-accent-purple"
              value={totalFiles.toLocaleString()}
              valueColor="text-accent-purple"
              label="Total Files"
            />
          </div>
        </GridSeam>

        <div class="dashboard-grid">
          <!-- Charts Row -->
          <div class="slab col-span-full lg:col-span-8">
            <div class="slab-header">
              <h3>Storage Growth Trend</h3>
            </div>
            <div class="slab-body--padded">
              {#if trendData.length > 0}
                <StorageTrendChart data={trendData} height={280} title="" />
              {:else}
                <EmptyState
                  iconName="TrendingUp"
                  title="No storage trend data"
                  description="Storage usage history will appear here once you have active recordings or VOD assets."
                  actionText="Manage Recordings"
                  onAction={() => goto(resolve("/recordings"))}
                />
              {/if}
            </div>
          </div>

          <div class="slab col-span-full lg:col-span-4">
            <div class="slab-header">
              <h3>Current Breakdown</h3>
            </div>
            <div class="slab-body--padded">
              <div class="h-[280px] flex flex-col justify-center">
                {#if breakdownData}
                  <StorageBreakdownChart data={breakdownData} height={200} />
                  
                  <div class="mt-4 grid grid-cols-2 gap-2 text-xs">
                    <div class="flex items-center gap-2">
                      <div class="w-2 h-2 rounded bg-blue-500"></div>
                      <span class="text-muted-foreground">DVR:</span>
                      <span class="font-mono ml-auto">{formatBytes(breakdownData.dvrBytes)}</span>
                    </div>
                    <div class="flex items-center gap-2">
                      <div class="w-2 h-2 rounded bg-purple-500"></div>
                      <span class="text-muted-foreground">Clips:</span>
                      <span class="font-mono ml-auto">{formatBytes(breakdownData.clipBytes)}</span>
                    </div>
                    <div class="flex items-center gap-2">
                      <div class="w-2 h-2 rounded bg-green-500"></div>
                      <span class="text-muted-foreground">VOD:</span>
                      <span class="font-mono ml-auto">{formatBytes(breakdownData.vodBytes)}</span>
                    </div>
                  </div>
                {:else}
                  <div class="flex items-center justify-center h-full">
                    <EmptyState
                      iconName="PieChart"
                      title="No storage data"
                      description="Storage breakdown will appear when you have data."
                    />
                  </div>
                {/if}
              </div>
            </div>
          </div>

          <!-- Note: Storage events are now per-stream via Stream.storageEventsConnection -->
        </div>
      </div>
    {/if}
  </div>
</div>