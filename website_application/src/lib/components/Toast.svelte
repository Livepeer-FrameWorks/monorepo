<script>
  import { toast } from '$lib/stores/toast.js';
  import { fly } from 'svelte/transition';
  
  $: toasts = $toast;
  
  function getToastIcon(type) {
    switch (type) {
      case 'success': return '✅';
      case 'error': return '❌';
      case 'warning': return '⚠️';
      case 'info': return 'ℹ️';
      default: return '📢';
    }
  }
  
  function getToastColors(type) {
    switch (type) {
      case 'success': 
        return 'bg-green-500/20 border-green-500/30 text-green-300';
      case 'error': 
        return 'bg-red-500/20 border-red-500/30 text-red-300';
      case 'warning': 
        return 'bg-yellow-500/20 border-yellow-500/30 text-yellow-300';
      case 'info': 
        return 'bg-blue-500/20 border-blue-500/30 text-blue-300';
      default: 
        return 'bg-tokyo-night-surface border-tokyo-night-selection text-tokyo-night-fg';
    }
  }
</script>

<!-- Toast Container -->
<div class="fixed top-4 right-4 z-50 space-y-2 pointer-events-none">
  {#each toasts as toastItem (toastItem.id)}
    <div
      class="pointer-events-auto rounded-lg border p-4 shadow-lg max-w-sm {getToastColors(toastItem.type)}"
      transition:fly={{ x: 300, duration: 300 }}
    >
      <div class="flex items-start space-x-3">
        <div class="text-lg">
          {getToastIcon(toastItem.type)}
        </div>
        <div class="flex-1 min-w-0">
          <p class="text-sm font-medium break-words">
            {toastItem.message}
          </p>
        </div>
        <button
          on:click={() => toast.remove(toastItem.id)}
          class="text-current opacity-60 hover:opacity-100 transition-opacity"
          aria-label="Close notification"
        >
          <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
          </svg>
        </button>
      </div>
    </div>
  {/each}
</div>