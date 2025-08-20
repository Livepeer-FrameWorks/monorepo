<script>
  import { healthService } from '$lib/graphql/services/health.js';

  export let bufferState = 'EMPTY';
  export let bufferHealth = null;
  export let size = 'md';

  $: colorClass = healthService.getBufferStateColor(bufferState);
  $: healthPercent = bufferHealth ? Math.round(bufferHealth * 100) : null;
  
  $: sizeClasses = {
    sm: 'w-4 h-4',
    md: 'w-6 h-6', 
    lg: 'w-8 h-8'
  };

  $: getStateDescription = (state) => {
    switch (state) {
      case 'FULL': return 'Buffer is full and healthy';
      case 'EMPTY': return 'Buffer has space available';
      case 'DRY': return 'Buffer is critically low';
      case 'RECOVER': return 'Buffer is recovering';
      default: return 'Buffer state unknown';
    }
  };

  $: getStateIcon = (state) => {
    switch (state) {
      case 'FULL': return '●●●●';
      case 'EMPTY': return '●●○○';
      case 'DRY': return '●○○○';
      case 'RECOVER': return '●●○○';
      default: return '○○○○';
    }
  };
</script>

<div class="flex items-center space-x-2">
  <div class="flex items-center space-x-1">
    <!-- Buffer state icon -->
    <div class={`${colorClass} font-mono ${sizeClasses[size]} flex items-center justify-center`}>
      <span class="text-xs">{getStateIcon(bufferState)}</span>
    </div>
    
    <!-- State label -->
    <div class="flex flex-col">
      <span class={`font-medium ${colorClass} text-sm`}>
        {bufferState}
      </span>
      {#if healthPercent !== null}
        <span class="text-xs text-tokyo-night-comment">
          {healthPercent}% healthy
        </span>
      {/if}
    </div>
  </div>

  <!-- Tooltip with description -->
  <div class="group relative">
    <div class="w-4 h-4 rounded-full bg-tokyo-night-selection flex items-center justify-center cursor-help">
      <span class="text-xs text-tokyo-night-comment">?</span>
    </div>
    <div class="absolute bottom-full left-1/2 transform -translate-x-1/2 mb-2 px-2 py-1 bg-tokyo-night-surface text-tokyo-night-fg text-xs rounded shadow-lg opacity-0 group-hover:opacity-100 transition-opacity z-10 whitespace-nowrap">
      {getStateDescription(bufferState)}
    </div>
  </div>
</div>