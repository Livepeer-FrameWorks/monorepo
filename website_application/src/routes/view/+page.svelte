<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import Player from '$lib/components/Player.svelte';
  import LoadingSpinner from '$lib/components/LoadingSpinner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  interface PlayerConfig {
    contentType: "live" | "clip" | "dvr";
    contentId: string;
    thumbnailUrl?: string | null;
    options: {
      autoplay: boolean;
      muted: boolean;
      controls: boolean;
      debug: boolean;
    };
  }

  let contentType = $state<"live" | "clip" | "dvr" | "">("");
  let contentId = $state("");
  let loading = $state(true);
  let error = $state("");
  let playerConfig = $state<PlayerConfig | null>(null);

  onMount(async () => {
    // Parse URL parameters
    const params = page.url.searchParams;
    const typeParam = params.get("type") || "";
    contentId = params.get("id") || "";

    // Validate required parameters
    if (!typeParam || !contentId) {
      error = "Missing required parameters: type and id";
      loading = false;
      return;
    }

    // Validate content type
    if (!["live", "clip", "dvr"].includes(typeParam)) {
      error = "Invalid content type. Must be live, clip, or dvr";
      loading = false;
      return;
    }

    contentType = typeParam as "live" | "clip" | "dvr";

    try {
      // Configure player based on content type
      playerConfig = {
        contentType,
        contentId,
        options: {
          autoplay: true,
          muted: true,
          controls: true,
          debug: false,
        },
      };

      // Add thumbnails for clips/dvr
      if (contentType !== 'live') {
        playerConfig.thumbnailUrl = null; // Will be resolved by the player
      }

      loading = false;
    } catch (err) {
      console.error('Error setting up player:', err);
      error = 'Failed to initialize player';
      loading = false;
    }
  });

  function goBack() {
    window.history.back();
  }
</script>

<svelte:head>
  <title>FrameWorks Viewer</title>
  <meta name="description" content="FrameWorks streaming viewer" />
</svelte:head>

<div class="min-h-screen bg-background">
  {#if loading}
    <div class="flex items-center justify-center min-h-screen">
      <LoadingSpinner />
    </div>
  {:else if error}
    <div class="flex items-center justify-center min-h-screen">
      <EmptyState 
        title="Error" 
        description={error}
        actionText="Go Back"
        onAction={goBack}
      />
    </div>
  {:else if playerConfig}
    <!-- Full-screen viewer layout -->
    <div class="relative w-full h-screen">
      <!-- Header bar (optional, can be hidden for full immersion) -->
      <div class="absolute top-0 left-0 right-0 z-50 bg-black bg-opacity-50 p-4">
        <div class="flex items-center justify-between">
          <button 
            onclick={goBack}
            class="text-foreground hover:text-muted-foreground flex items-center"
          >
            <svg class="w-6 h-6 mr-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"></path>
            </svg>
            Back
          </button>
          
          <div class="text-white text-sm">
            {contentType === 'live' ? 'Live Stream' : contentType === 'clip' ? 'Clip' : 'DVR Recording'}
          </div>

          <div class="text-white text-xs opacity-75">
            ID: {contentId}
          </div>
        </div>
      </div>

      <!-- Player container -->
      <div class="w-full h-full">
        <Player 
          contentId={playerConfig.contentId}
          contentType={playerConfig.contentType}
          thumbnailUrl={playerConfig.thumbnailUrl}
          options={playerConfig.options}
        />
      </div>
    </div>
  {/if}
</div>

<style>
  /* Hide scrollbars for full-screen experience */
  :global(body) {
    overflow: hidden;
  }
  
  /* Ensure player takes full viewport */
  :global(.player-container) {
    width: 100vw !important;
    height: 100vh !important;
  }
</style>
