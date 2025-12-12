<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";
  import { getIconComponent } from "$lib/iconUtils";
  import type { SystemHealth$result } from "$houdini";

  interface NodePerformanceMetric {
    nodeId: string;
    avgCpuUsage?: number;
    avgMemoryUsage?: number;
    avgDiskUsage?: number;
    avgShmUsage?: number;
  }

  type SystemHealthEvent = NonNullable<SystemHealth$result["systemHealth"]>;

  interface Props {
    nodePerformanceMetrics: NodePerformanceMetric[];
    systemHealth: Record<string, { event: SystemHealthEvent; ts: Date }>;
  }

  let { nodePerformanceMetrics, systemHealth }: Props = $props();

  function getStatusBadgeClass(status: string | null | undefined) {
    switch (status?.toLowerCase()) {
      case "healthy":
        return "border-success/40 bg-success/10 text-success";
      case "degraded":
        return "border-warning/40 bg-warning/10 text-warning";
      case "unhealthy":
        return "border-rose-500/40 bg-rose-500/10 text-rose-300";
      default:
        return "border-muted-foreground/40 bg-muted-foreground/10 text-muted-foreground";
    }
  }
</script>

{#if nodePerformanceMetrics.length > 0}
  <div class="overflow-x-auto border border-border/50">
    <table class="w-full text-sm">
      <thead>
        <tr
          class="border-b border-border/50 bg-background/40 text-left text-muted-foreground"
        >
          <th class="py-3 px-4">Node</th>
          <th class="py-3 px-4">Status</th>
          <th class="py-3 px-4">CPU (Avg)</th>
          <th class="py-3 px-4">Memory (Avg)</th>
          <th class="py-3 px-4">SHM (Avg)</th>
          <th class="py-3 px-4">Disk (Avg)</th>
        </tr>
      </thead>
      <tbody>
        {#each nodePerformanceMetrics.slice(0, 10) as metric (metric.nodeId)}
          {@const health = systemHealth[metric.nodeId]}
          <tr
            class="border-b border-border/20 transition hover:bg-background/40"
          >
            <td class="py-3 px-4 font-mono text-xs">{metric.nodeId}</td>
            <td class="py-3 px-4">
              {#if health}
                <Badge variant="outline" class={getStatusBadgeClass(health.event.status)}>
                  {health.event.status}
                </Badge>
              {:else}
                <span class="text-muted-foreground">-</span>
              {/if}
            </td>
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
                class={metric.avgShmUsage && metric.avgShmUsage > 80
                  ? "text-error"
                  : metric.avgShmUsage && metric.avgShmUsage > 60
                    ? "text-warning"
                    : "text-success"}
              >
                {metric.avgShmUsage?.toFixed(1) || 0}%
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
{:else}
  {@const BarChartIcon = getIconComponent("BarChart")}
  <div class="text-center py-8">
    <BarChartIcon class="w-12 h-12 text-muted-foreground mx-auto mb-4" />
    <p class="text-muted-foreground">No performance data available</p>
  </div>
{/if}
