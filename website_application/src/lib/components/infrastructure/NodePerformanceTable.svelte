<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";

  interface NodePerformanceMetric {
    nodeId: string;
    avgCpuUsage?: number;
    avgMemoryUsage?: number;
    avgDiskUsage?: number;
  }

  interface Props {
    nodePerformanceMetrics: NodePerformanceMetric[];
  }

  let { nodePerformanceMetrics }: Props = $props();
</script>

{#if nodePerformanceMetrics.length > 0}
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-lg font-medium text-foreground">
        Node Performance Details
      </h3>
      <Badge variant="outline" class="uppercase tracking-wide text-[0.65rem]">
        Showing {Math.min(nodePerformanceMetrics.length, 10)} of {nodePerformanceMetrics.length}
      </Badge>
    </div>
    <div
      class="overflow-x-auto border border-border/50"
    >
      <table class="w-full text-sm">
        <thead>
          <tr
            class="border-b border-border/50 bg-background/40 text-left text-muted-foreground"
          >
            <th class="py-3 px-4">Node</th>
            <th class="py-3 px-4">CPU</th>
            <th class="py-3 px-4">Memory</th>
            <th class="py-3 px-4">Disk</th>
          </tr>
        </thead>
        <tbody>
          {#each nodePerformanceMetrics.slice(0, 10) as metric (metric.nodeId)}
            <tr
              class="border-b border-border/20 transition hover:bg-background/40"
            >
              <td class="py-3 px-4 font-mono text-xs">{metric.nodeId}</td>
              <td class="py-3 px-4">
                <span
                  class={metric.avgCpuUsage && metric.avgCpuUsage > 80
                    ? "text-error"
                    : metric.avgCpuUsage && metric.avgCpuUsage > 60
                      ? "text-warning"
                      : "text-success"}
                >
                  {metric.avgCpuUsage?.toFixed(1) || 0}%
                </span>
              </td>
              <td class="py-3 px-4">
                <span
                  class={metric.avgMemoryUsage && metric.avgMemoryUsage > 80
                    ? "text-error"
                    : metric.avgMemoryUsage && metric.avgMemoryUsage > 60
                      ? "text-warning"
                      : "text-success"}
                >
                  {metric.avgMemoryUsage?.toFixed(1) || 0}%
                </span>
              </td>
              <td class="py-3 px-4">
                <span
                  class={metric.avgDiskUsage && metric.avgDiskUsage > 80
                    ? "text-error"
                    : metric.avgDiskUsage && metric.avgDiskUsage > 60
                      ? "text-warning"
                      : "text-success"}
                >
                  {metric.avgDiskUsage?.toFixed(1) || 0}%
                </span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
{:else}
  {@const BarChartIcon = getIconComponent("BarChart")}
  <div class="text-center py-8">
    <BarChartIcon class="w-12 h-12 text-muted-foreground mx-auto mb-4" />
    <p class="text-muted-foreground">No performance data available</p>
  </div>
{/if}
