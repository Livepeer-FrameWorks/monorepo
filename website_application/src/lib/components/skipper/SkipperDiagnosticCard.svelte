<script lang="ts">
  interface Props {
    toolName: string;
    payload: Record<string, unknown>;
  }

  let { toolName, payload }: Props = $props();

  const status = (payload.status as string) ?? "unknown";
  const metrics = (payload.metrics as Record<string, unknown>) ?? {};
  const analysis = (payload.analysis as string) ?? "";
  const recommendations = (payload.recommendations as string[]) ?? [];

  const statusColors: Record<string, string> = {
    healthy: "bg-emerald-500",
    warning: "bg-amber-500",
    critical: "bg-red-500",
  };

  const statusLabels: Record<string, string> = {
    healthy: "Healthy",
    warning: "Warning",
    critical: "Critical",
  };

  const toolLabels: Record<string, string> = {
    diagnose_rebuffering: "Rebuffering Diagnosis",
    diagnose_buffer_health: "Buffer Health",
    diagnose_packet_loss: "Packet Loss Analysis",
    diagnose_routing: "Routing Diagnostics",
    get_stream_health_summary: "Stream Health Summary",
    get_anomaly_report: "Anomaly Detection",
  };

  function formatMetricKey(key: string): string {
    return key
      .replace(/_/g, " ")
      .replace(/([a-z])([A-Z])/g, "$1 $2")
      .replace(/^./, (c) => c.toUpperCase());
  }

  function formatMetricValue(value: unknown): string {
    if (typeof value === "number") {
      if (Number.isInteger(value)) return value.toLocaleString();
      return value.toFixed(2);
    }
    if (typeof value === "boolean") return value ? "Yes" : "No";
    if (value === null || value === undefined) return "N/A";
    return String(value);
  }
</script>

<div class="rounded-lg border border-border bg-card text-sm">
  <div class="flex items-center justify-between border-b border-border px-4 py-2.5">
    <div class="flex items-center gap-2">
      <span class="h-2.5 w-2.5 rounded-full {statusColors[status] ?? 'bg-muted-foreground'}"></span>
      <span class="font-semibold text-foreground">
        {toolLabels[toolName] ?? toolName}
      </span>
    </div>
    <span
      class="rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wider
        {status === 'healthy'
        ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
        : status === 'warning'
          ? 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400'
          : status === 'critical'
            ? 'border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400'
            : 'border-border bg-muted text-muted-foreground'}"
    >
      {statusLabels[status] ?? status}
    </span>
  </div>

  {#if Object.keys(metrics).length > 0}
    <div class="border-b border-border px-4 py-2.5">
      <div
        class="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground"
      >
        Metrics
      </div>
      <div class="space-y-1">
        {#each Object.entries(metrics) as [key, value] (key)}
          <div class="flex items-center justify-between gap-4 text-xs">
            <span class="text-muted-foreground">{formatMetricKey(key)}</span>
            <span class="font-mono text-foreground">{formatMetricValue(value)}</span>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if analysis}
    <div class="border-b border-border px-4 py-2.5">
      <div
        class="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground"
      >
        Analysis
      </div>
      <p class="text-xs leading-relaxed text-foreground">{analysis}</p>
    </div>
  {/if}

  {#if recommendations.length > 0}
    <div class="px-4 py-2.5">
      <div
        class="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground"
      >
        Recommendations
      </div>
      <ul class="space-y-1">
        {#each recommendations as rec (rec)}
          <li class="flex items-start gap-2 text-xs text-foreground">
            <span class="mt-0.5 text-muted-foreground/60">&#x2022;</span>
            <span>{rec}</span>
          </li>
        {/each}
      </ul>
    </div>
  {/if}
</div>
