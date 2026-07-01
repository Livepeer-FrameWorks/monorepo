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
    delta?: string | null;
    deltaTrend?: "up" | "down" | null;
    sparkline?: number[] | null;
  }

  let {
    icon: Icon,
    iconColor,
    value,
    valueColor,
    label,
    subtitle = null,
    statusIndicator = null,
    delta = null,
    deltaTrend = null,
    sparkline = null,
  }: Props = $props();

  // Inline sparkline path (decorative trend); maps the series into a 100x26 box.
  const sparkPoints = $derived.by(() => {
    if (!sparkline || sparkline.length < 2) return "";
    const W = 100;
    const H = 26;
    const min = Math.min(...sparkline);
    const max = Math.max(...sparkline);
    const span = max - min || 1;
    const step = W / (sparkline.length - 1);
    return sparkline
      .map(
        (v, i) => `${(i * step).toFixed(1)},${(H - ((v - min) / span) * (H - 4) - 2).toFixed(1)}`
      )
      .join(" ");
  });
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
    <div class="flex items-baseline gap-2">
      <div class="text-lg font-bold {valueColor} leading-none">
        {value}
      </div>
      {#if delta}
        <span
          class="text-[11px] font-semibold {deltaTrend === 'down'
            ? 'text-destructive'
            : 'text-success'}"
        >
          {delta}
        </span>
      {/if}
    </div>
    <div class="text-xs text-muted-foreground font-medium mt-1 truncate" title={label}>{label}</div>
    {#if subtitle}
      <div class="text-[10px] text-muted-foreground/70 truncate">{subtitle}</div>
    {/if}
  </div>

  {#if sparkPoints}
    <svg
      class="ml-auto h-7 w-20 shrink-0 opacity-80 {valueColor}"
      viewBox="0 0 100 26"
      preserveAspectRatio="none"
      aria-hidden="true"
    >
      <polyline
        points={sparkPoints}
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </svg>
  {/if}
</div>
