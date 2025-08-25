<script>
  import { onMount, onDestroy } from 'svelte';

  export let contentId;
  export let contentType; // 'live', 'clip', or 'dvr'
  export let thumbnailUrl = null;
  export let options = {};

  let playerContainer;
  let player;
  let loading = true;
  let error = '';

  // Default options
  const defaultOptions = {
    gatewayUrl: import.meta.env.VITE_GRAPHQL_HTTP_URL,
    autoplay: true,
    muted: true,
    controls: true,
    preferredProtocol: 'auto',
    analytics: {
      enabled: true,
      sessionTracking: true
    },
    debug: false,
    verboseLogging: false
  };

  // Merge options
  const playerOptions = { ...defaultOptions, ...options };

  // Get auth token if available
  if (typeof window !== 'undefined') {
    const token = localStorage.getItem('token');
    if (token) {
      playerOptions.authToken = token;
    }
  }

  onMount(async () => {
    try {
      // Import the NPM player package
      const { Player: FrameWorksPlayer } = await import('@livepeer-frameworks/player');

      // Initialize the NPM player - it handles everything internally
      player = new FrameWorksPlayer(playerContainer, {
        contentId,
        contentType,
        thumbnailUrl,
        options: playerOptions
      });

      loading = false;

    } catch (err) {
      console.error('Failed to initialize player:', err);
      error = err.message || 'Failed to load player';
      loading = false;
    }
  });

  onDestroy(() => {
    if (player && typeof player.destroy === 'function') {
      player.destroy();
    }
  });
</script>

<div class="player-wrapper w-full h-full relative bg-black">
  {#if loading}
    <div class="absolute inset-0 flex items-center justify-center bg-black">
      <div class="text-center text-white">
        <div class="animate-spin rounded-full h-12 w-12 border-b-2 border-white mx-auto mb-4"></div>
        <p>Loading player...</p>
      </div>
    </div>
  {/if}

  {#if error}
    <div class="absolute inset-0 flex items-center justify-center bg-black">
      <div class="text-center text-white max-w-md px-4">
        <div class="mb-4">
          <svg class="w-16 h-16 mx-auto text-red-500" fill="currentColor" viewBox="0 0 20 20">
            <path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 102 0V6a1 1 0 00-1-1z" clip-rule="evenodd" />
          </svg>
        </div>
        <h3 class="text-lg font-semibold mb-2">Player Error</h3>
        <p class="text-sm text-gray-300">{error}</p>
      </div>
    </div>
  {/if}

  <!-- NPM Player container - it handles everything internally -->
  <div 
    bind:this={playerContainer}
    class="player-container w-full h-full"
    class:hidden={loading || error}
  ></div>
</div>

<style>
  .player-wrapper {
    background: #000;
  }
</style>