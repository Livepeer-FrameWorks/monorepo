<script>
  import { healthService } from '$lib/graphql/services/health.js';

  export let healthScore = 0;
  export let size = 'md';
  export let showLabel = true;

  $: formattedScore = Math.round(healthScore * 100);
  $: colorClass = healthService.getHealthScoreColor(healthScore);
  
  $: sizeClasses = {
    sm: 'w-8 h-8 text-xs',
    md: 'w-12 h-12 text-sm',
    lg: 'w-16 h-16 text-base'
  };

  $: labelSizes = {
    sm: 'text-xs',
    md: 'text-sm',
    lg: 'text-base'
  };

  // Calculate stroke-dasharray for circular progress
  $: circumference = 2 * Math.PI * 18; // radius = 18
  $: strokeDasharray = `${formattedScore * circumference / 100} ${circumference}`;
</script>

<div class="flex items-center space-x-2">
  <div class={`relative ${sizeClasses[size]}`}>
    <!-- Background circle -->
    <svg class="w-full h-full transform -rotate-90" viewBox="0 0 40 40">
      <circle
        cx="20"
        cy="20"
        r="18"
        stroke="currentColor"
        stroke-width="2"
        fill="transparent"
        class="text-tokyo-night-selection"
      />
      <!-- Progress circle -->
      <circle
        cx="20"
        cy="20"
        r="18"
        stroke="currentColor"
        stroke-width="2"
        fill="transparent"
        stroke-linecap="round"
        stroke-dasharray={strokeDasharray}
        class={colorClass}
      />
    </svg>
    
    <!-- Score text -->
    <div class="absolute inset-0 flex items-center justify-center">
      <span class={`font-semibold ${colorClass} ${labelSizes[size]}`}>
        {formattedScore}
      </span>
    </div>
  </div>

  {#if showLabel}
    <div class="flex flex-col">
      <span class={`font-medium ${colorClass} ${labelSizes[size]}`}>
        Health Score
      </span>
      <span class={`text-tokyo-night-comment ${labelSizes[size]}`}>
        {#if formattedScore >= 90}
          Excellent
        {:else if formattedScore >= 70}
          Good
        {:else if formattedScore >= 50}
          Fair
        {:else}
          Poor
        {/if}
      </span>
    </div>
  {/if}
</div>