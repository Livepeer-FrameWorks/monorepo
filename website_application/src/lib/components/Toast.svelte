<script lang="ts">
  import { toast } from '$lib/stores/toast.js';
  import { fly } from 'svelte/transition';
  import { getIconComponent } from '$lib/iconUtils';
  
  let toasts = $derived($toast);
  
  type ToastType = 'success' | 'error' | 'warning' | 'info';

  function getToastIcon(type: ToastType) {
    const iconMap: Record<ToastType, string> = {
      success: 'CheckCircle',
      error: 'XCircle',
      warning: 'AlertTriangle',
      info: 'Info'
    };
    return getIconComponent(iconMap[type] || 'Info');
  }

  function getToastColors(type: ToastType) {
    // Use subtle, dark backgrounds with colored left border accent
    switch (type) {
      case 'success':
        return 'bg-card border-l-4 border-l-success border-y-border border-r-border text-foreground';
      case 'error':
        return 'bg-card border-l-4 border-l-error border-y-border border-r-border text-foreground';
      case 'warning':
        return 'bg-card border-l-4 border-l-warning border-y-border border-r-border text-foreground';
      case 'info':
        return 'bg-card border-l-4 border-l-primary border-y-border border-r-border text-foreground';
      default:
        return 'bg-card border border-border text-foreground';
    }
  }

  function getIconColor(type: ToastType) {
    switch (type) {
      case 'success': return 'text-success';
      case 'error': return 'text-error';
      case 'warning': return 'text-warning';
      case 'info': return 'text-primary';
      default: return 'text-muted-foreground';
    }
  }
</script>

<!-- Toast Container -->
<div class="fixed bottom-4 right-4 z-50 space-y-2 pointer-events-none">
  {#each toasts as toastItem (toastItem.id)}
    {@const IconComponent = getToastIcon(toastItem.type)}
    {@const CloseIcon = getIconComponent('X')}
    <div
      class="pointer-events-auto rounded-md p-4 shadow-xl max-w-sm {getToastColors(toastItem.type)}"
      transition:fly={{ y: 100, duration: 300 }}
    >
      <div class="flex items-start gap-3">
        <div class="shrink-0 {getIconColor(toastItem.type)}">
          <IconComponent class="w-5 h-5" />
        </div>
        <div class="flex-1 min-w-0">
          <p class="text-sm font-medium break-words">
            {toastItem.message}
          </p>
        </div>
        <button
          onclick={() => toast.remove(toastItem.id)}
          class="cursor-pointer text-muted-foreground hover:text-foreground hover:bg-muted rounded p-1 -m-1 transition-colors shrink-0"
          aria-label="Close notification"
        >
          <CloseIcon class="w-4 h-4" />
        </button>
      </div>
    </div>
  {/each}
</div>