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
    statusIndicator?: StatusIndicator | null;
  }

  let {
    icon: Icon,
    iconColor,
    value,
    valueColor,
    label,
    statusIndicator = null,
  }: Props = $props();
</script>

<div class="metric-card text-center relative">
  {#if statusIndicator}
    <div class="absolute top-3 right-3">
      <div class="flex items-center space-x-1 text-xs">
        <div
          class="w-2 h-2 rounded-full {statusIndicator.connected
            ? 'bg-tokyo-night-green animate-pulse'
            : 'bg-tokyo-night-red'}"
        ></div>
        <span class="text-tokyo-night-comment">{statusIndicator.label}</span>
      </div>
    </div>
  {/if}

  <div class="text-3xl mb-2">
    <Icon class="w-8 h-8 {iconColor} mx-auto" />
  </div>

  <div class="text-2xl font-bold {valueColor} mb-1">
    {value}
  </div>

  <div class="text-sm text-tokyo-night-comment">{label}</div>
</div>
