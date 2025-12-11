<script lang="ts">
  import { cn } from "$lib/utils";
  import { getIconComponent, type IconName } from "$lib/iconUtils";
  import type { ComponentType } from "svelte";

  type StatusSize = "small" | "normal" | "large";
  type StatusColor = "green" | "blue" | "yellow" | "red" | "gray";

  interface Props {
    status?: string | null;
    size?: StatusSize;
    showLabel?: boolean;
    pulse?: boolean;
    class?: string;
  }

  interface StatusConfig {
    color: StatusColor;
    label: string;
    icon: ComponentType | null;
  }

  const makeConfig = (
    color: StatusColor,
    label: string,
    icon: IconName
  ): StatusConfig => ({
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
    paused: makeConfig("yellow", "Paused", "PauseCircle" as IconName),
    warning: makeConfig("yellow", "Warning", "AlertTriangle"),
    degraded: makeConfig("yellow", "Degraded", "AlertTriangle"),
    offline: makeConfig("red", "Offline", "CircleX" as IconName),
    error: makeConfig("red", "Error", "XCircle"),
    failed: makeConfig("red", "Failed", "XCircle"),
    disconnected: makeConfig("red", "Disconnected", "WifiOff" as IconName),
    unhealthy: makeConfig("red", "Unhealthy", "XCircle"),
    stopped: makeConfig("gray", "Stopped", "StopCircle" as IconName),
    inactive: makeConfig("gray", "Inactive", "Circle"),
    disabled: makeConfig("gray", "Disabled", "CircleSlash" as IconName),
    maintenance: makeConfig("gray", "Maintenance", "Wrench" as IconName),
    unknown: makeConfig("gray", "Unknown", "HelpCircle"),
  };

  const colorClasses: Record<
    StatusColor,
    { bg: string; text: string; border: string }
  > = {
    green: {
      bg: "bg-success/20",
      text: "text-success",
      border: "border-success/30",
    },
    blue: {
      bg: "bg-accent/20",
      text: "text-accent",
      border: "border-accent/30",
    },
    yellow: {
      bg: "bg-warning/20",
      text: "text-warning",
      border: "border-warning/30",
    },
    red: {
      bg: "bg-error/20",
      text: "text-error",
      border: "border-error/30",
    },
    gray: {
      bg: "bg-muted/50",
      text: "text-muted-foreground",
      border: "border-muted-foreground/30",
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
    class: className,
  }: Props = $props();

  const normalizedStatus = $derived(status?.toString().toLowerCase() ?? "unknown");
  const statusConfig = $derived(
    statusConfigs[normalizedStatus] ?? statusConfigs.unknown
  );
  const IconComponent = $derived(statusConfig.icon);
  const color = $derived(colorClasses[statusConfig.color]);
  const sizeClasses = $derived(sizeMap[size] ?? sizeMap.normal);
</script>

<span
  class={cn(
    "inline-flex items-center rounded-full font-medium border",
    sizeClasses.container,
    color.bg,
    color.text,
    color.border,
    className
  )}
>
  {#if IconComponent}
    <IconComponent
      class={cn(sizeClasses.icon, sizeClasses.spacing, pulse && "animate-pulse")}
    />
  {/if}
  {#if showLabel}
    {statusConfig.label}
  {/if}
</span>
