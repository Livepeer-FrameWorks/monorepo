<script>
  import { onMount } from 'svelte';
  import { errorBoundary } from '$lib/stores/errorBoundary.js';
  import { toast } from '$lib/stores/toast.js';

  let errorState = { hasError: false, error: null, errorMessage: null, errorInfo: null };
  
  // Subscribe to error boundary store
  errorBoundary.subscribe((state) => {
    errorState = state;
    
    // Also show toast notification for errors
    if (state.hasError && state.errorMessage) {
      toast.error(state.errorMessage);
    }
  });
  
  onMount(() => {
    // Set up global error handlers
    const handleUnhandledError = (event) => {
      console.error('Unhandled error:', event.error);
      errorBoundary.setError(
        event.error, 
        'An unexpected error occurred', 
        event.error?.message || 'Unknown error'
      );
      event.preventDefault();
    };
    
    const handleUnhandledRejection = (event) => {
      console.error('Unhandled promise rejection:', event.reason);
      errorBoundary.setError(
        new Error(event.reason), 
        'An unexpected error occurred', 
        typeof event.reason === 'string' ? event.reason : 'Promise rejection'
      );
      event.preventDefault();
    };
    
    // Add global error listeners
    window.addEventListener('error', handleUnhandledError);
    window.addEventListener('unhandledrejection', handleUnhandledRejection);
    
    // Cleanup on unmount
    return () => {
      window.removeEventListener('error', handleUnhandledError);
      window.removeEventListener('unhandledrejection', handleUnhandledRejection);
    };
  });

  function handleReload() {
    errorBoundary.clearError();
    window.location.reload();
  }

  function handleDismiss() {
    errorBoundary.clearError();
  }
</script>

{#if errorState.hasError}
  <!-- Error Overlay -->
  <div class="fixed inset-0 bg-black/50 backdrop-blur-sm flex items-center justify-center z-50 p-4">
    <div class="bg-tokyo-night-bg-light border border-tokyo-night-red/50 rounded-lg p-6 max-w-lg w-full shadow-2xl">
      <div class="flex items-center space-x-3 mb-4">
        <div class="text-3xl">⚠️</div>
        <div>
          <h2 class="text-xl font-semibold text-tokyo-night-red mb-1">
            Something went wrong
          </h2>
          <p class="text-tokyo-night-comment text-sm">
            An error occurred while loading this page
          </p>
        </div>
      </div>

      <div class="bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded p-4 mb-6">
        <p class="text-tokyo-night-fg mb-2 font-medium">
          {errorState.errorMessage}
        </p>
        {#if errorState.errorInfo}
          <details class="text-sm">
            <summary class="cursor-pointer text-tokyo-night-comment hover:text-tokyo-night-fg">
              Technical details
            </summary>
            <pre class="mt-2 text-xs text-tokyo-night-comment font-mono whitespace-pre-wrap break-words">
{errorState.errorInfo}
            </pre>
          </details>
        {/if}
      </div>

      <div class="flex justify-end space-x-3">
        <button
          class="btn-secondary"
          on:click={handleDismiss}
        >
          Dismiss
        </button>
        <button
          class="btn-primary"
          on:click={handleReload}
        >
          Reload Page
        </button>
      </div>

      <div class="mt-4 pt-4 border-t border-tokyo-night-fg-gutter">
        <p class="text-xs text-tokyo-night-comment">
          If this error persists, please contact support with the technical details above.
        </p>
      </div>
    </div>
  </div>
{/if}