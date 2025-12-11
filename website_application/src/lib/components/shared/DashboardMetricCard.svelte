<script lang="ts">
  import type { ComponentType } from "svelte";

  interface StatusIndicator {
    connected: boolean;
    label: string;
  }

  interface Props {
    icon: ComponentType;
    iconColor: string;
    value: string | number;
    valueColor: string;
    label: string;
    subtitle?: string | null;
    statusIndicator?: StatusIndicator | null;
  }

  let {
    icon: Icon,
    iconColor,
    value,
    valueColor,
    label,
    subtitle = null,
    statusIndicator = null,
  }: Props = $props();
</script>

<div class="metric-card text-center relative">
  {#if statusIndicator}
    <div class="absolute top-3 right-3">
      <div class="flex items-center space-x-1 text-xs">
        <div
          class="w-2 h-2 rounded-full {statusIndicator.connected
            ? 'bg-success animate-pulse'
            : 'bg-destructive'}"
        ></div>
        <span class="text-muted-foreground">{statusIndicator.label}</span>
      </div>
    </div>
  {/if}

  <div class="text-3xl mb-2">
    <Icon class="w-8 h-8 {iconColor} mx-auto" />
  </div>

  <div class="text-2xl font-bold {valueColor} mb-1">
    {value}
  </div>

  <div class="text-sm text-muted-foreground">{label}</div>
  {#if subtitle}
    <div class="text-xs text-muted-foreground/70 mt-0.5">{subtitle}</div>
  {/if}
</div>
