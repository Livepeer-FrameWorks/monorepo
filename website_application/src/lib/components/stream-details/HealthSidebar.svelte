<script lang="ts">
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { Button } from "$lib/components/ui/button";
  import { getIconComponent } from "$lib/iconUtils";
  import BufferStateIndicator from "$lib/components/health/BufferStateIndicator.svelte";

  type HealthState = "HEALTHY" | "UNHEALTHY" | "IDLE" | "LOADING";

  interface HealthCheck {
    name: string;
    status: "ok" | "warning" | "error" | "unknown";
    label?: string;
  }

  interface StreamHealth {
    bufferState?: string | null;
    bufferHealth?: number | null;
    qualityTier?: string | null;
    bitrate?: number | null;
    fps?: number | null;
    issuesDescription?: string | null;
    // Rich health data from STREAM_BUFFER subscription
    streamBufferMs?: number | null; // Actual buffer depth in milliseconds
    streamJitterMs?: number | null; // Max jitter across tracks
    maxKeepawaMs?: number | null; // Viewer lag from live edge
    hasIssues?: boolean | null;
    mistIssues?: string | null; // Raw Mist issue string
    trackCount?: number | null;
  }

  interface StreamSummaryMetrics {
    packetsSent?: number | null;
    packetsLost?: number | null;
    packetsRetrans?: number | null;
    packetLossRate?: number | null;
  }

  interface Props {
    streamId: string;
    isLive: boolean;
    health: StreamHealth | null;
    analytics?: StreamSummaryMetrics | null;
    collapsed?: boolean;
    onToggle?: () => void;
  }

  let { streamId, isLive, health, analytics = null, collapsed = false, onToggle }: Props = $props();

  // Derive global health state
  let globalHealth = $derived.by((): HealthState => {
    if (!isLive) return "IDLE";
    if (!health) return "LOADING"; // Stream is live but health data hasn't loaded yet

    // Check for critical issues
    if (health.bufferState === "DRY") return "UNHEALTHY";
    if ((health.bufferHealth ?? 1) < 0.3) return "UNHEALTHY";

    // Check for warnings
    if (health.bufferState === "EMPTY") return "HEALTHY"; // EMPTY is normal

    return "HEALTHY";
  });

  // Build health checks list
  let healthChecks = $derived.by((): HealthCheck[] => {
    if (!health || !isLive) return [];

    const checks: HealthCheck[] = [];

    // Buffer check
    const bufferStatus =
      health.bufferState === "FULL"
        ? "ok"
        : health.bufferState === "DRY"
          ? "error"
          : health.bufferState === "RECOVER"
            ? "warning"
            : "ok";
    checks.push({
      name: "Buffer",
      status: bufferStatus,
      label: health.bufferState?.toLowerCase() ?? "unknown",
    });

    // Jitter check (from real-time subscription)
    if (health.streamJitterMs !== undefined && health.streamJitterMs !== null) {
      const jitter = health.streamJitterMs;
      checks.push({
        name: "Jitter",
        status: jitter > 100 ? "error" : jitter > 50 ? "warning" : "ok",
        label: `${jitter}ms`,
      });
    }

    // Transcoding check (based on quality tier presence)
    checks.push({
      name: "Transcoding",
      status: health.qualityTier ? "ok" : "unknown",
      label: health.qualityTier ?? "N/A",
    });

    // Packet loss check (from stream analytics)
    if (analytics?.packetLossRate !== undefined && analytics.packetLossRate !== null) {
      const lossRate = analytics.packetLossRate;
      checks.push({
        name: "Packet Loss",
        status: lossRate > 0.05 ? "error" : lossRate > 0.01 ? "warning" : "ok",
        label: `${(lossRate * 100).toFixed(2)}%`,
      });
    }

    // Issues check (from real-time subscription)
    if (health.hasIssues) {
      checks.push({
        name: "Issues",
        status: "error",
        label: health.mistIssues ?? "detected",
      });
    }

    return checks;
  });

  function getHealthColor(state: HealthState): string {
    switch (state) {
      case "HEALTHY":
        return "text-success";
      case "UNHEALTHY":
        return "text-error";
      case "LOADING":
        return "text-muted-foreground animate-pulse";
      case "IDLE":
        return "text-muted-foreground";
    }
  }

  function getHealthBg(state: HealthState): string {
    switch (state) {
      case "HEALTHY":
        return "bg-success/10";
      case "UNHEALTHY":
        return "bg-error/10";
      case "LOADING":
        return "bg-muted/30";
      case "IDLE":
        return "bg-muted/50";
    }
  }

  function getCheckIcon(status: HealthCheck["status"]): string {
    switch (status) {
      case "ok":
        return "CheckCircle";
      case "warning":
        return "AlertTriangle";
      case "error":
        return "XCircle";
      default:
        return "HelpCircle";
    }
  }

  function getCheckColor(status: HealthCheck["status"]): string {
    switch (status) {
      case "ok":
        return "text-success";
      case "warning":
        return "text-warning";
      case "error":
        return "text-error";
      default:
        return "text-muted-foreground";
    }
  }

  function navigateToHealth() {
    goto(resolve(`/streams/${streamId}/health`));
  }

  const ActivityIcon = getIconComponent("Activity");
  const ChevronRightIcon = getIconComponent("ChevronRight");
  const ChevronLeftIcon = getIconComponent("ChevronLeft");
</script>

{#if !collapsed}
  <div class="h-full flex flex-col border-l border-border bg-brand-surface-muted">
    <!-- Header -->
    <div class="p-4 border-b border-border flex items-center justify-between">
      <div class="flex items-center gap-2">
        <ActivityIcon class="w-4 h-4 {getHealthColor(globalHealth)}" />
        <span class="font-medium text-sm">Health</span>
      </div>
      {#if onToggle}
        <button
          onclick={onToggle}
          class="p-1 hover:bg-muted/50 transition-colors cursor-pointer"
          title="Collapse sidebar"
        >
          <ChevronRightIcon class="w-4 h-4" />
        </button>
      {/if}
    </div>

    <!-- Global Status -->
    <div class="p-4 {getHealthBg(globalHealth)}">
      <div class="text-center">
        <span class="text-2xl font-bold {getHealthColor(globalHealth)}">
          {globalHealth}
        </span>
        {#if !isLive}
          <p class="text-xs text-muted-foreground mt-1">Stream is offline</p>
        {:else if globalHealth === "LOADING"}
          <p class="text-xs text-muted-foreground mt-1">Fetching health data...</p>
        {/if}
      </div>
    </div>

    <!-- Health Checks -->
    {#if isLive && healthChecks.length > 0}
      <div class="p-4 space-y-3 border-b border-border">
        {#each healthChecks as check (check.name)}
          {@const Icon = getIconComponent(getCheckIcon(check.status))}
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <Icon class="w-4 h-4 {getCheckColor(check.status)}" />
              <span class="text-sm text-foreground">{check.name}</span>
            </div>
            <span class="text-xs font-mono {getCheckColor(check.status)}">
              {check.label}
            </span>
          </div>
        {/each}
      </div>
    {/if}

    <!-- Buffer State Visual -->
    {#if isLive && health}
      <div class="p-4 border-b border-border">
        <div class="text-xs text-muted-foreground uppercase tracking-wide mb-2">Buffer State</div>
        <BufferStateIndicator
          bufferState={health.bufferState ?? "unknown"}
          bufferHealth={health.bufferHealth}
          size="md"
        />
      </div>
    {/if}

    <!-- Buffer Depth & Jitter (from real-time subscription) -->
    {#if isLive && (health?.streamBufferMs || health?.streamJitterMs || health?.maxKeepawaMs)}
      <div class="p-4 border-b border-border">
        <div class="text-xs text-muted-foreground uppercase tracking-wide mb-2">
          Real-time Metrics
        </div>
        <div class="space-y-2">
          {#if health.streamBufferMs !== undefined && health.streamBufferMs !== null}
            <div class="flex justify-between text-sm">
              <span class="text-muted-foreground">Buffer Depth</span>
              <span class="font-mono text-info">{health.streamBufferMs}ms</span>
            </div>
          {/if}
          {#if health.streamJitterMs !== undefined && health.streamJitterMs !== null}
            <div class="flex justify-between text-sm">
              <span class="text-muted-foreground">Jitter</span>
              <span
                class="font-mono {health.streamJitterMs > 100
                  ? 'text-error'
                  : health.streamJitterMs > 50
                    ? 'text-warning'
                    : 'text-success'}"
              >
                {health.streamJitterMs}ms
              </span>
            </div>
          {/if}
          {#if health.maxKeepawaMs !== undefined && health.maxKeepawaMs !== null}
            <div class="flex justify-between text-sm">
              <span class="text-muted-foreground">Viewer Lag</span>
              <span class="font-mono text-muted-foreground">{health.maxKeepawaMs}ms</span>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Ingest Rate (mini display) -->
    {#if isLive && health?.bitrate}
      <div class="p-4 border-b border-border">
        <div class="text-xs text-muted-foreground uppercase tracking-wide mb-2">Ingest Rate</div>
        <div class="flex items-baseline gap-2">
          <span class="text-2xl font-mono text-info">
            {(health.bitrate / 1000).toFixed(1)}
          </span>
          <span class="text-sm text-muted-foreground">Mbps</span>
        </div>
        {#if health.fps}
          <div class="text-xs text-muted-foreground mt-1">
            {health.fps.toFixed(0)} fps • {health.qualityTier ?? "Unknown"}
          </div>
        {/if}
      </div>
    {/if}

    <!-- Issues -->
    {#if health?.issuesDescription}
      <div class="p-4 border-b border-border bg-warning/5">
        <div class="text-xs text-muted-foreground uppercase tracking-wide mb-2">Issues</div>
        <p class="text-sm text-warning">{health.issuesDescription}</p>
      </div>
    {/if}

    <!-- Spacer -->
    <div class="flex-1"></div>

    <!-- Footer Action -->
    <div class="p-4 border-t border-border">
      <Button variant="ghost" class="w-full justify-between" onclick={navigateToHealth}>
        <span>Full Health Dashboard</span>
        <ChevronRightIcon class="w-4 h-4" />
      </Button>
    </div>
  </div>
{:else}
  <!-- Collapsed state: just a thin bar with toggle -->
  <div class="h-full flex flex-col items-center border-l border-border bg-brand-surface-muted w-10">
    <button
      onclick={onToggle}
      class="p-2 hover:bg-muted/50 transition-colors mt-2 cursor-pointer"
      title="Expand health sidebar"
    >
      <ChevronLeftIcon class="w-4 h-4" />
    </button>
    <div class="mt-4">
      <ActivityIcon class="w-5 h-5 {getHealthColor(globalHealth)}" />
    </div>
    {#if isLive && health?.bitrate}
      <div class="mt-4 text-xs font-mono text-muted-foreground writing-mode-vertical">
        {(health.bitrate / 1000).toFixed(1)}Mbps
      </div>
    {/if}
  </div>
{/if}

<style>
  .writing-mode-vertical {
    writing-mode: vertical-rl;
    text-orientation: mixed;
  }
</style>
