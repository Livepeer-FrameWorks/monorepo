<script>
  /** @type {'stream' | 'clip' | 'metric' | 'analytics' | 'infrastructure' | 'generic'} */
  export let variant = 'generic';
  /** @type {string} */
  export let className = '';
</script>

{#if variant === 'stream'}
  <!-- Stream Card Skeleton -->
  <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg border border-tokyo-night-fg-gutter {className}">
    <div class="flex items-center justify-between mb-3">
      <div class="skeleton-text-lg w-2/3"></div>
      <div class="flex items-center space-x-2">
        <div class="skeleton w-2 h-2 rounded-full"></div>
        <div class="skeleton-text-sm w-16"></div>
      </div>
    </div>
    
    <div class="grid grid-cols-2 gap-4 text-sm mb-3">
      <div class="space-y-1">
        <div class="skeleton-text-sm w-12"></div>
        <div class="skeleton-text w-16"></div>
      </div>
      <div class="space-y-1">
        <div class="skeleton-text-sm w-12"></div>
        <div class="skeleton-text w-16"></div>
      </div>
    </div>

    <div class="pt-3 border-t border-tokyo-night-fg-gutter">
      <div class="skeleton-text-sm w-full"></div>
    </div>
  </div>

{:else if variant === 'clip'}
  <!-- Clip Card Skeleton -->
  <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection {className}">
    <div class="mb-3">
      <div class="skeleton-text-lg w-3/4 mb-1"></div>
      <div class="skeleton-text-sm w-1/2"></div>
    </div>
    
    <div class="skeleton-text w-full mb-3 h-8"></div>
    
    <div class="space-y-2 text-sm">
      <div class="flex justify-between">
        <div class="skeleton-text-sm w-16"></div>
        <div class="skeleton-text-sm w-12"></div>
      </div>
      <div class="flex justify-between">
        <div class="skeleton-text-sm w-16"></div>
        <div class="skeleton-text-sm w-12"></div>
      </div>
      <div class="flex justify-between">
        <div class="skeleton-text-sm w-12"></div>
        <div class="skeleton w-16 h-5 rounded-full"></div>
      </div>
      <div class="flex justify-between">
        <div class="skeleton-text-sm w-16"></div>
        <div class="skeleton-text-sm w-20"></div>
      </div>
    </div>
  </div>

{:else if variant === 'metric'}
  <!-- Metric Card Skeleton -->
  <div class="glow-card p-6 text-center {className}">
    <div class="skeleton w-8 h-8 mx-auto mb-3 rounded"></div>
    <div class="skeleton-text-lg w-16 mx-auto mb-1"></div>
    <div class="skeleton-text-sm w-20 mx-auto"></div>
  </div>

{:else if variant === 'analytics'}
  <!-- Analytics Card Skeleton -->
  <div class="bg-tokyo-night-surface rounded-lg p-6 {className}">
    <div class="skeleton-text-lg w-48 mb-4"></div>
    <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
      {#each Array(4) as _}
        <div class="text-center">
          <div class="skeleton-text-lg w-12 mx-auto mb-1"></div>
          <div class="skeleton-text-sm w-16 mx-auto"></div>
        </div>
      {/each}
    </div>
  </div>

{:else if variant === 'infrastructure'}
  <!-- Infrastructure Node/Cluster Card Skeleton -->
  <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection {className}">
    <div class="flex items-center justify-between mb-3">
      <div class="skeleton-text-lg w-24"></div>
      <div class="skeleton w-16 h-6 rounded-full"></div>
    </div>
    
    <div class="space-y-2">
      <div class="flex justify-between">
        <div class="skeleton-text-sm w-12"></div>
        <div class="skeleton-text-sm w-16"></div>
      </div>
      <div class="flex justify-between">
        <div class="skeleton-text-sm w-16"></div>
        <div class="skeleton-text-sm w-12"></div>
      </div>
      <div class="flex justify-between">
        <div class="skeleton-text-sm w-14"></div>
        <div class="skeleton-text-sm w-10"></div>
      </div>
    </div>
  </div>

{:else}
  <!-- Generic Card Skeleton -->
  <div class="skeleton-card {className}"></div>
{/if}