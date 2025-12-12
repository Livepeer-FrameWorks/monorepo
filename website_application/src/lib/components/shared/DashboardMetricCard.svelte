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

<div class="h-full p-4 relative flex items-center gap-4">
  {#if statusIndicator}
    <div class="absolute top-2 right-2">
      <div class="flex items-center space-x-1 text-[10px]">
        <div
          class="w-1.5 h-1.5 rounded-full {statusIndicator.connected
            ? 'bg-success animate-pulse'
            : 'bg-destructive'}"
        ></div>
        <span class="text-muted-foreground">{statusIndicator.label}</span>
      </div>
    </div>
  {/if}

  <div class="w-10 h-10 shrink-0 rounded-lg bg-muted/50 flex items-center justify-center">
    <Icon class="w-5 h-5 {iconColor}" />
  </div>

  <div class="flex flex-col min-w-0">
    <div class="text-lg font-bold {valueColor} leading-none">
      {value}
    </div>
    <div class="text-xs text-muted-foreground font-medium mt-1 truncate" title={label}>{label}</div>
    {#if subtitle}
      <div class="text-[10px] text-muted-foreground/70 truncate">{subtitle}</div>
    {/if}
  </div>
</div>
