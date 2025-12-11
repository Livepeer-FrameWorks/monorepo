<script lang="ts">
  type SizeKey = 'sm' | 'md' | 'lg';

  interface Props {
    healthScore?: number | null;
    size?: SizeKey;
    showLabel?: boolean;
  }

  let { healthScore = 0, size = 'md', showLabel = true }: Props = $props();

  function getHealthScoreColor(score: number): string {
    if (score >= 0.9) return "text-success";
    if (score >= 0.7) return "text-warning";
    if (score >= 0.5) return "text-warning-alt";
    return "text-error";
  }

  let formattedScore = $derived(Math.round((healthScore ?? 0) * 100));
  let colorClass = $derived(getHealthScoreColor(healthScore ?? 0));

  const sizeClasses: Record<SizeKey, string> = {
    sm: 'w-8 h-8 text-xs',
    md: 'w-12 h-12 text-sm',
    lg: 'w-16 h-16 text-base'
  };

  const labelSizes: Record<SizeKey, string> = {
    sm: 'text-xs',
    md: 'text-sm',
    lg: 'text-base'
  };

  // Calculate stroke-dasharray for circular progress
  let circumference = $derived(2 * Math.PI * 18); // radius = 18
  let strokeDasharray = $derived(`${formattedScore * circumference / 100} ${circumference}`);
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
        class="text-muted"
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
      <span class={`text-muted-foreground ${labelSizes[size]}`}>
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