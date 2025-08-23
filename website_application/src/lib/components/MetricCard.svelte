<script>
  import { formatNumber, formatBytes, formatDuration, formatPercentage } from '$lib/utils/formatters.js';
  
  export let title = '';
  export let value = null;
  export let previousValue = null;
  export let unit = '';
  export let format = 'number'; // 'number', 'bytes', 'duration', 'percentage'
  export let icon = null;
  export let trend = null; // 'up', 'down', 'stable'
  export let loading = false;
  export let error = null;
  export let size = 'normal'; // 'small', 'normal', 'large'
  
  // Calculate change percentage if previousValue is provided
  $: changePercent = previousValue && value !== null && previousValue !== 0 
    ? ((value - previousValue) / previousValue) * 100 
    : null;
  
  $: formattedValue = formatValue(value, format, unit);
  $: changeDirection = changePercent ? (changePercent > 0 ? 'up' : 'down') : null;
  
  function formatValue(val, fmt, unit) {
    if (val === null || val === undefined) return 'N/A';
    
    switch (fmt) {
      case 'bytes':
        return formatBytes(val);
      case 'duration':
        return formatDuration(val);
      case 'percentage':
        return val.toFixed(1) + '%';
      case 'number':
      default:
        return formatNumber(val) + (unit ? ` ${unit}` : '');
    }
  }
  
  function getTrendIcon(direction) {
    switch (direction) {
      case 'up': return '↗';
      case 'down': return '↘';
      case 'stable': return '→';
      default: return '';
    }
  }
  
  function getTrendColor(direction, positive = true) {
    if (!direction) return 'text-slate-400';
    
    if (positive) {
      return direction === 'up' ? 'text-green-400' : 
             direction === 'down' ? 'text-red-400' : 
             'text-slate-400';
    } else {
      return direction === 'up' ? 'text-red-400' : 
             direction === 'down' ? 'text-green-400' : 
             'text-slate-400';
    }
  }
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
    {#if $$slots.default}
      <div class="mt-3 pt-3 border-t border-slate-700">
        <slot />
      </div>
    {/if}
  {/if}
</div>