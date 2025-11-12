<script lang="ts">
  import { getIconComponent, type IconName } from "$lib/iconUtils";
  import type { ComponentType } from "svelte";

  type StatusSize = "small" | "normal" | "large";
  type StatusColor = "green" | "blue" | "yellow" | "red" | "gray";

  interface Props {
    status?: string | null;
    size?: StatusSize;
    showLabel?: boolean;
    pulse?: boolean;
  }

  interface StatusConfig {
    color: StatusColor;
    label: string;
    icon: ComponentType | null;
  }

  const makeConfig = (color: StatusColor, label: string, icon: IconName) => ({
    color,
    label,
    icon: getIconComponent(icon),
  });

  const statusConfigs: Record<string, StatusConfig> = {
    live: makeConfig("green", "Live", "Circle"),
    online: makeConfig("green", "Online", "Circle"),
    active: makeConfig("green", "Active", "Circle"),
    healthy: makeConfig("green", "Healthy", "CheckCircle"),
    connected: makeConfig("green", "Connected", "Wifi"),
    recording: makeConfig("blue", "Recording", "Circle"),
    processing: makeConfig("blue", "Processing", "RefreshCw"),
    pending: makeConfig("blue", "Pending", "Clock"),
    buffering: makeConfig("yellow", "Buffering", "Clock"),
    paused: makeConfig("yellow", "Paused", "PauseCircle"),
    warning: makeConfig("yellow", "Warning", "AlertTriangle"),
    degraded: makeConfig("yellow", "Degraded", "AlertTriangle"),
    offline: makeConfig("red", "Offline", "CircleX"),
    error: makeConfig("red", "Error", "XCircle"),
    failed: makeConfig("red", "Failed", "XCircle"),
    disconnected: makeConfig("red", "Disconnected", "WifiOff"),
    unhealthy: makeConfig("red", "Unhealthy", "XCircle"),
    stopped: makeConfig("gray", "Stopped", "StopCircle"),
    inactive: makeConfig("gray", "Inactive", "Circle"),
    disabled: makeConfig("gray", "Disabled", "CircleSlash"),
    maintenance: makeConfig("gray", "Maintenance", "Wrench"),
    unknown: makeConfig("gray", "Unknown", "HelpCircle"),
  };

  const colorClasses: Record<
    StatusColor,
    { bg: string; text: string; border: string }
  > = {
    green: {
      bg: "bg-tokyo-night-green/20",
      text: "text-tokyo-night-green",
      border: "border-tokyo-night-green/30",
    },
    blue: {
      bg: "bg-tokyo-night-blue/20",
      text: "text-tokyo-night-blue",
      border: "border-tokyo-night-blue/30",
    },
    yellow: {
      bg: "bg-tokyo-night-yellow/20",
      text: "text-tokyo-night-yellow",
      border: "border-tokyo-night-yellow/30",
    },
    red: {
      bg: "bg-tokyo-night-red/20",
      text: "text-tokyo-night-red",
      border: "border-tokyo-night-red/30",
    },
    gray: {
      bg: "bg-tokyo-night-comment/20",
      text: "text-tokyo-night-comment",
      border: "border-tokyo-night-comment/30",
    },
  };

  const sizeMap: Record<
    StatusSize,
    { container: string; icon: string; spacing: string }
  > = {
    small: {
      container: "px-2 py-0.5 text-xs",
      icon: "w-3 h-3",
      spacing: "mr-1",
    },
    normal: {
      container: "px-2.5 py-0.5 text-xs",
      icon: "w-3.5 h-3.5",
      spacing: "mr-1.5",
    },
    large: {
      container: "px-3 py-1 text-sm",
      icon: "w-4 h-4",
      spacing: "mr-2",
    },
  };

  let {
    status = "",
    size = "normal",
    showLabel = true,
    pulse = false,
  }: Props = $props();

  const normalizedStatus = $derived(
    () => status?.toString().toLowerCase() ?? "unknown",
  );
  const statusConfig = $derived(
    () => statusConfigs[normalizedStatus()] ?? statusConfigs.unknown,
  );
  const IconComponent = $derived(() => statusConfig().icon);
  const color = $derived(() => colorClasses[statusConfig().color]);
  const sizeClasses = $derived(() => sizeMap[size] ?? sizeMap.normal);
</script>

<span
  class="inline-flex items-center {sizeClasses.container} rounded-full font-medium {color.bg} {color.text} border {color.border}"
>
  {#if IconComponent}
    <IconComponent
      class="{sizeClasses.icon} {sizeClasses.spacing} {pulse ? 'animate-pulse' : ''}"
    />
  {/if}
  {#if showLabel}
    {statusConfig().label}
  {/if}
</span>
