<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";

  interface NodePerformanceMetric {
    nodeId: string;
    avgCpuUsage?: number;
    avgMemoryUsage?: number;
    avgHealthScore?: number;
    peakActiveStreams?: number;
    avgStreamLoad?: number;
  }

  interface Props {
    nodePerformanceMetrics: NodePerformanceMetric[];
  }

  let { nodePerformanceMetrics }: Props = $props();
</script>

{#if nodePerformanceMetrics.length > 0}
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-lg font-medium text-tokyo-night-fg">
        Node Performance Details
      </h3>
      <Badge variant="outline" class="uppercase tracking-wide text-[0.65rem]">
        Showing {Math.min(nodePerformanceMetrics.length, 10)} of {nodePerformanceMetrics.length}
      </Badge>
    </div>
    <div
      class="overflow-x-auto rounded-lg border border-border/40 bg-card/40"
    >
      <table class="w-full text-sm">
        <thead>
          <tr
            class="border-b border-border/50 bg-background/40 text-left text-tokyo-night-comment"
          >
            <th class="py-3 px-4">Node</th>
            <th class="py-3 px-4">CPU</th>
            <th class="py-3 px-4">Memory</th>
            <th class="py-3 px-4">Health Score</th>
            <th class="py-3 px-4">Peak Streams</th>
            <th class="py-3 px-4">Avg Load</th>
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
                    ? "text-red-400"
                    : metric.avgCpuUsage && metric.avgCpuUsage > 60
                      ? "text-yellow-400"
                      : "text-green-400"}
                >
                  {metric.avgCpuUsage?.toFixed(1) || 0}%
                </span>
              </td>
              <td class="py-3 px-4">
                <span
                  class={metric.avgMemoryUsage && metric.avgMemoryUsage > 80
                    ? "text-red-400"
                    : metric.avgMemoryUsage && metric.avgMemoryUsage > 60
                      ? "text-yellow-400"
                      : "text-green-400"}
                >
                  {metric.avgMemoryUsage?.toFixed(1) || 0}%
                </span>
              </td>
              <td class="py-3 px-4">
                <span
                  class={metric.avgHealthScore && metric.avgHealthScore > 0.8
                    ? "text-green-400"
                    : metric.avgHealthScore && metric.avgHealthScore > 0.6
                      ? "text-yellow-400"
                      : "text-red-400"}
                >
                  {Math.round((metric.avgHealthScore ?? 0) * 100) || 0}%
                </span>
              </td>
              <td class="py-3 px-4 font-semibold"
                >{metric.peakActiveStreams || 0}</td
              >
              <td class="py-3 px-4 text-tokyo-night-comment">
                {metric.avgStreamLoad?.toFixed(2) || "0.00"}
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
    <BarChartIcon class="w-12 h-12 text-tokyo-night-comment mx-auto mb-4" />
    <p class="text-tokyo-night-comment">No performance data available</p>
  </div>
{/if}
