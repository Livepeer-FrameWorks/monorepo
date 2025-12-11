<script lang="ts">
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { Button } from "$lib/components/ui/button";
  import { getIconComponent } from "$lib/iconUtils";
  import BufferStateIndicator from "$lib/components/health/BufferStateIndicator.svelte";

  type HealthState = "HEALTHY" | "UNHEALTHY" | "IDLE";

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
    packetLossPercentage?: number | null;
    issuesDescription?: string | null;
  }

  interface Props {
    streamId: string;
    streamName: string;
    isLive: boolean;
    health: StreamHealth | null;
    collapsed?: boolean;
    onToggle?: () => void;
  }

  let { streamId, streamName, isLive, health, collapsed = false, onToggle }: Props = $props();

  // Derive global health state
  let globalHealth = $derived.by((): HealthState => {
    if (!isLive) return "IDLE";
    if (!health) return "IDLE";

    // Check for critical issues
    if (health.bufferState === "DRY") return "UNHEALTHY";
    if ((health.packetLossPercentage ?? 0) > 0.05) return "UNHEALTHY";
    if ((health.bufferHealth ?? 1) < 0.3) return "UNHEALTHY";

    // Check for warnings
    if (health.bufferState === "EMPTY") return "HEALTHY"; // EMPTY is normal
    if ((health.packetLossPercentage ?? 0) > 0.02) return "HEALTHY"; // Minor loss is ok

    return "HEALTHY";
  });

  // Build health checks list
  let healthChecks = $derived.by((): HealthCheck[] => {
    if (!health || !isLive) return [];

    const checks: HealthCheck[] = [];

    // Buffer check
    const bufferStatus = health.bufferState === "FULL" ? "ok"
      : health.bufferState === "DRY" ? "error"
      : health.bufferState === "RECOVER" ? "warning"
      : "ok";
    checks.push({
      name: "Buffer",
      status: bufferStatus,
      label: health.bufferState?.toLowerCase() ?? "unknown",
    });

    // Transcoding check (based on quality tier presence)
    checks.push({
      name: "Transcoding",
      status: health.qualityTier ? "ok" : "unknown",
      label: health.qualityTier ?? "N/A",
    });

    // Delivery check (based on packet loss)
    const lossPercent = (health.packetLossPercentage ?? 0) * 100;
    const deliveryStatus = lossPercent > 5 ? "error" : lossPercent > 2 ? "warning" : "ok";
    checks.push({
      name: "Delivery",
      status: deliveryStatus,
      label: lossPercent > 0 ? `${lossPercent.toFixed(1)}% loss` : "OK",
    });

    return checks;
  });

  function getHealthColor(state: HealthState): string {
    switch (state) {
      case "HEALTHY": return "text-success";
      case "UNHEALTHY": return "text-error";
      case "IDLE": return "text-muted-foreground";
    }
  }

  function getHealthBg(state: HealthState): string {
    switch (state) {
      case "HEALTHY": return "bg-success/10";
      case "UNHEALTHY": return "bg-error/10";
      case "IDLE": return "bg-muted/50";
    }
  }

  function getCheckIcon(status: HealthCheck["status"]): string {
    switch (status) {
      case "ok": return "CheckCircle";
      case "warning": return "AlertTriangle";
      case "error": return "XCircle";
      default: return "HelpCircle";
    }
  }

  function getCheckColor(status: HealthCheck["status"]): string {
    switch (status) {
      case "ok": return "text-success";
      case "warning": return "text-warning";
      case "error": return "text-error";
      default: return "text-muted-foreground";
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
        {/if}
      </div>
    </div>

    <!-- Health Checks -->
    {#if isLive && healthChecks.length > 0}
      <div class="p-4 space-y-3 border-b border-border">
        {#each healthChecks as check}
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
          bufferState={health.bufferState ?? "EMPTY"}
          bufferHealth={health.bufferHealth}
          size="md"
        />
      </div>
    {/if}

    <!-- Ingest Rate (mini display) -->
    {#if isLive && health?.bitrate}
      <div class="p-4 border-b border-border">
        <div class="text-xs text-muted-foreground uppercase tracking-wide mb-2">Ingest Rate</div>
        <div class="flex items-baseline gap-2">
          <span class="text-2xl font-mono text-info">
            {(health.bitrate / 1000000).toFixed(1)}
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
      <Button
        variant="ghost"
        class="w-full justify-between"
        onclick={navigateToHealth}
      >
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
        {(health.bitrate / 1000000).toFixed(1)}Mbps
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
