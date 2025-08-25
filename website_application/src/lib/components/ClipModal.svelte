<script>
  import { onMount, onDestroy } from 'svelte';
  import Player from './Player.svelte';
  import { getIconComponent } from '$lib/iconUtils.js';

  export let clip = null;
  export let onClose = () => {};

  let modalElement;
  let playerContainer;

  // Close modal on Escape key
  function handleKeydown(event) {
    if (event.key === 'Escape') {
      onClose();
    }
  }

  // Close modal when clicking outside
  function handleBackdropClick(event) {
    if (event.target === modalElement) {
      onClose();
    }
  }

  onMount(() => {
    // Add event listeners
    document.addEventListener('keydown', handleKeydown);
    document.body.style.overflow = 'hidden'; // Prevent background scrolling
  });

  onDestroy(() => {
    // Clean up event listeners
    document.removeEventListener('keydown', handleKeydown);
    document.body.style.overflow = 'auto'; // Restore scrolling
  });

  function formatDuration(seconds) {
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    return `${minutes}:${remainingSeconds.toString().padStart(2, '0')}`;
  }
</script>

{#if clip}
  <!-- Modal backdrop -->
  <div 
    bind:this={modalElement}
    class="fixed inset-0 bg-black bg-opacity-75 flex items-center justify-center z-50 p-4"
    on:click={handleBackdropClick}
  >
    <!-- Modal content -->
    <div class="bg-tokyo-night-bg-light rounded-lg shadow-2xl max-w-4xl w-full max-h-[90vh] overflow-hidden">
      <!-- Modal header -->
      <div class="flex items-center justify-between p-6 border-b border-tokyo-night-fg-gutter">
        <div class="flex-1 min-w-0">
          <h2 class="text-xl font-semibold text-tokyo-night-fg truncate">
            {clip.title}
          </h2>
          {#if clip.description}
            <p class="text-sm text-tokyo-night-comment mt-1 line-clamp-2">
              {clip.description}
            </p>
          {/if}
        </div>
        <button
          on:click={onClose}
          class="ml-4 text-tokyo-night-comment hover:text-tokyo-night-fg p-2 rounded-lg hover:bg-tokyo-night-bg-highlight transition-colors"
          title="Close (Esc)"
        >
          <svelte:component this={getIconComponent('X')} class="w-5 h-5" />
        </button>
      </div>

      <!-- Player area -->
      <div class="relative bg-black aspect-video">
        <Player 
          contentId={clip.playbackId || clip.id}
          contentType="clip"
          thumbnailUrl={clip.thumbnailUrl}
          options={{
            autoplay: true,
            muted: false,
            controls: true,
            preferredProtocol: 'auto',
            analytics: {
              enabled: true,
              sessionTracking: false
            }
          }}
        />
      </div>

      <!-- Clip info -->
      <div class="p-6 border-t border-tokyo-night-fg-gutter">
        <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
          <div>
            <p class="text-tokyo-night-comment">Duration</p>
            <p class="font-medium text-tokyo-night-fg">
              {formatDuration(clip.duration || (clip.endTime - clip.startTime))}
            </p>
          </div>
          <div>
            <p class="text-tokyo-night-comment">Start Time</p>
            <p class="font-medium text-tokyo-night-fg">
              {formatDuration(clip.startTime || 0)}
            </p>
          </div>
          <div>
            <p class="text-tokyo-night-comment">Status</p>
            <p class="font-medium text-tokyo-night-fg">
              <span class="px-2 py-1 text-xs rounded-full bg-tokyo-night-green bg-opacity-20 text-tokyo-night-green">
                {clip.status || 'Available'}
              </span>
            </p>
          </div>
          <div>
            <p class="text-tokyo-night-comment">Created</p>
            <p class="font-medium text-tokyo-night-fg">
              {clip.createdAt ? new Date(clip.createdAt).toLocaleDateString() : 'N/A'}
            </p>
          </div>
        </div>
        
        {#if clip.streamName}
          <div class="mt-4 pt-4 border-t border-tokyo-night-fg-gutter">
            <p class="text-sm text-tokyo-night-comment">
              From stream: <span class="text-tokyo-night-fg font-medium">{clip.streamName}</span>
            </p>
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .line-clamp-2 {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>