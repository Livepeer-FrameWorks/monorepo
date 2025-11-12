<script lang="ts">
  import { formatNumber, formatBytes, formatDuration } from "$lib/utils/formatters.js";
  
  interface Props {
    title?: string;
    value?: number | string | null;
    previousValue?: number | null;
    unit?: string;
    format?: "number" | "bytes" | "duration" | "percentage";
    icon?: string | null;
    trend?: "up" | "down" | "stable" | null;
    loading?: boolean;
    error?: string | null;
    size?: "small" | "normal" | "large";
    children?: import("svelte").Snippet | null;
  }

  let {
    title = '',
    value = null,
    previousValue = null,
    unit = '',
    format = "number",
    icon = null,
    trend = null,
    loading = false,
    error = null,
    size = 'normal',
    children
  }: Props = $props();
  
  
  
  /**
   * @param {number | null} val
   * @param {string} fmt
   * @param {string} unit
   */
  function formatValue(
    val: number | string | null,
    fmt: Props["format"],
    unit: string,
  ) {
    if (val === null || val === undefined) return "N/A";
    
    switch (fmt) {
      case "bytes":
        return typeof val === "number" ? formatBytes(val) : String(val);
      case "duration":
        return typeof val === "number" ? formatDuration(val) : String(val);
      case "percentage":
        return typeof val === "number"
          ? `${val.toFixed(1)}%`
          : String(val);
      case "number":
      default:
        if (typeof val === "number") {
          return `${formatNumber(val)}${unit ? ` ${unit}` : ""}`;
        }
        const numericValue = Number(val);
        return Number.isFinite(numericValue)
          ? `${formatNumber(numericValue)}${unit ? ` ${unit}` : ""}`
          : `${String(val)}${unit ? ` ${unit}` : ""}`;
    }
  }
  
  /**
   * @param {string | null} direction
   */
  function getTrendIcon(direction: "up" | "down" | "stable" | null) {
    switch (direction) {
      case "up":
        return "↗";
      case "down":
        return "↘";
      case "stable":
        return "→";
      default:
        return "";
    }
  }
  
  /**
   * @param {string | null} direction
   * @param {boolean} positive
   */
  function getTrendColor(direction: "up" | "down" | "stable" | null, positive = true) {
    if (!direction) return "text-slate-400";
    
    if (positive) {
      return direction === "up"
        ? "text-green-400"
        : direction === "down"
          ? "text-red-400"
          : "text-slate-400";
    } else {
      return direction === "up"
        ? "text-red-400"
        : direction === "down"
          ? "text-green-400"
          : "text-slate-400";
    }
  }
  // Calculate change percentage if previousValue is provided
  let changePercent = $derived(
    typeof previousValue === "number" &&
      typeof value === "number" &&
      previousValue !== 0
      ? ((value - previousValue) / previousValue) * 100
      : null,
  );
  let formattedValue = $derived(formatValue(value, format, unit));
  let changeDirection = $derived(
    changePercent ? (changePercent > 0 ? "up" : "down") : null,
  );
</script>

<div class="bg-slate-800 border border-slate-700 rounded-lg p-{size === 'small' ? '4' : size === 'large' ? '8' : '6'} hover:border-slate-600 transition-colors">
  {#if loading}
    <div class="animate-pulse">
      <div class="flex items-center justify-between mb-2">
        <div class="h-4 bg-slate-600 rounded w-24"></div>
        {#if icon}
          <div class="h-5 w-5 bg-slate-600 rounded"></div>
        {/if}
      </div>
      <div class="h-8 bg-slate-600 rounded w-32 mb-2"></div>
      {#if changePercent !== null}
        <div class="h-3 bg-slate-600 rounded w-16"></div>
      {/if}
    </div>
  {:else if error}
    <div class="text-center py-4">
      <div class="text-red-400 text-sm font-medium mb-1">Error</div>
      <div class="text-slate-500 text-xs">{error}</div>
    </div>
  {:else}
    <!-- Header -->
    <div class="flex items-center justify-between mb-2">
      <h3 class="text-sm font-medium text-slate-400 {size === 'small' ? 'text-xs' : ''}">
        {title}
      </h3>
      {#if icon}
        <div class="text-slate-500 {size === 'small' ? 'text-sm' : 'text-lg'}">
          <!-- eslint-disable-next-line svelte/no-at-html-tags -->
          {@html icon}
        </div>
      {/if}
    </div>

    <!-- Value -->
    <div class="flex items-baseline justify-between">
      <div class="text-{size === 'small' ? 'xl' : size === 'large' ? '3xl' : '2xl'} font-bold text-slate-100 leading-tight">
        {formattedValue}
      </div>
      
      <!-- Trend indicator -->
      {#if trend || changeDirection}
        <div class="flex items-center text-sm">
          <span class="{getTrendColor(trend || changeDirection)} flex items-center">
            <span class="mr-1">{getTrendIcon(trend || changeDirection)}</span>
            {#if changePercent !== null}
              {Math.abs(changePercent).toFixed(1)}%
            {/if}
          </span>
        </div>
      {/if}
    </div>

    <!-- Additional info -->
    {#if children}
      <div class="mt-3 pt-3 border-t border-slate-700">
        {@render children?.()}
      </div>
    {/if}
  {/if}
</div>
