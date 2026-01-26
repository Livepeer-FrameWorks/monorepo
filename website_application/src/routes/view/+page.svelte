<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Player from '$lib/components/Player.svelte';
  import LoadingSpinner from '$lib/components/LoadingSpinner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { Button } from '$lib/components/ui/button';
  import type { PlayerMetadata } from '@livepeer-frameworks/player-svelte';

  interface PlayerConfig {
    contentType?: "live" | "clip" | "dvr" | "vod";
    contentId: string;
    thumbnailUrl?: string | null;
    options: {
      autoplay: boolean;
      muted: boolean;
      controls: boolean;
      debug: boolean;
      devMode?: boolean;
    };
  }

  let contentType = $state<"live" | "clip" | "dvr" | "vod" | null>(null);
  let contentId = $state("");
  let loading = $state(true);
  let error = $state("");
  let playerConfig = $state<PlayerConfig | null>(null);
  let streamMetadata = $state<PlayerMetadata | null>(null);

  // Derived display values from metadata
  let displayTitle = $derived(streamMetadata?.title || (contentType === 'live' ? 'Live Stream' : contentType === 'clip' ? 'Clip' : contentType === 'vod' ? 'VOD Asset' : contentType === 'dvr' ? 'DVR Recording' : 'Playback'));
  let videoTrack = $derived(streamMetadata?.tracks?.find(t => t.type === 'video'));
  let audioTrack = $derived(streamMetadata?.tracks?.find(t => t.type === 'audio'));
  let resolutionLabel = $derived(videoTrack ? `${videoTrack.width}x${videoTrack.height}` : null);
  let codecLabel = $derived(videoTrack?.codec || null);
  let fpsLabel = $derived(videoTrack?.fps ? `${videoTrack.fps}fps` : null);
  let bitrateLabel = $derived(videoTrack?.bitrate ? `${(videoTrack.bitrate / 1000).toFixed(0)} kbps` : null);

  function handleMetadata(metadata: PlayerMetadata) {
    streamMetadata = metadata;
    if (!contentType) {
      const resolved = (metadata.contentType || "").toLowerCase();
      if (resolved === "live" || resolved === "clip" || resolved === "dvr" || resolved === "vod") {
        contentType = resolved as "live" | "clip" | "dvr" | "vod";
      } else if (metadata.isLive === true) {
        contentType = "live";
      } else if (metadata.isLive === false) {
        contentType = "vod";
      }
    }
  }

  onMount(async () => {
    // Parse URL parameters
    const params = page.url.searchParams;
    const typeParam = (params.get("type") || "").toLowerCase();
    contentId = params.get("id") || "";

    // Validate required parameters
    if (!contentId) {
      error = "Missing required parameter: id";
      loading = false;
      return;
    }

    try {
      if (["live", "clip", "dvr", "vod"].includes(typeParam)) {
        contentType = typeParam as "live" | "clip" | "dvr" | "vod";
      }

      // Configure player based on content type
      playerConfig = {
        contentType: contentType || undefined,
        contentId,
        options: {
          autoplay: true,
          muted: true,
          controls: true,
          debug: true,
          devMode: true,
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
    if (window.history.length > 1) {
      window.history.back();
    } else {
      goto('/');
    }
  }
</script>

<svelte:head>
  <title>Viewing {displayTitle} - FrameWorks</title>
</svelte:head>

<div class="flex flex-col h-full overflow-y-auto p-4 md:p-6 bg-background">
  {#if loading}
    <div class="flex items-center justify-center flex-1 h-full min-h-[400px]">
      <LoadingSpinner />
    </div>
  {:else if error}
    <div class="flex items-center justify-center flex-1 h-full min-h-[400px]">
      <EmptyState 
        title="Error" 
        description={error}
        actionText="Go Back"
        onAction={goBack}
      />
    </div>
  {:else if playerConfig}
    <div class="max-w-7xl mx-auto w-full space-y-6">
      <!-- Header Slab -->
      <div class="slab h-auto">
        <div class="slab-header flex items-center justify-between">
          <div class="flex items-center gap-3">
            <Button variant="ghost" size="sm" onclick={goBack} class="gap-2">
              <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 19l-7-7 7-7m8 14l-7-7 7-7" />
              </svg>
              Back
            </Button>
            <div class="h-4 w-px bg-[hsl(var(--tn-fg-gutter)/0.3)]"></div>
            <h1 class="text-sm font-semibold uppercase tracking-wide text-foreground">
              {displayTitle}
            </h1>
          </div>
          
          {#if contentType === 'live' && streamMetadata?.viewers !== undefined}
            <div class="flex items-center gap-2 px-3 py-1 bg-[hsl(var(--tn-bg-dark)/0.5)] rounded border border-[hsl(var(--tn-fg-gutter)/0.3)]">
              <span class="w-2 h-2 bg-[hsl(var(--tn-red))] rounded-full animate-pulse"></span>
              <span class="text-xs font-medium text-[hsl(var(--tn-red))] uppercase tracking-wider">
                {streamMetadata.viewers} Viewer{streamMetadata.viewers !== 1 ? 's' : ''}
              </span>
            </div>
          {/if}
        </div>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <!-- Main Player Column -->
        <div class="lg:col-span-2 space-y-6">
          <div class="slab overflow-hidden bg-black shadow-xl border-none">
            <!-- Responsive Container -->
            <div class="relative w-full h-[65vh] min-h-[480px]">
              <Player
                contentId={playerConfig.contentId}
                contentType={playerConfig.contentType}
                thumbnailUrl={playerConfig.thumbnailUrl}
                options={playerConfig.options}
                onMetadata={handleMetadata}
              />
            </div>
          </div>
        </div>

        <!-- Info Column -->
        <div class="space-y-6">
          <!-- Metadata Slab -->
          <div class="slab">
            <div class="slab-header">
              <h3 class="font-medium text-[hsl(var(--tn-fg-dark))]">Stream Metadata</h3>
            </div>
            <div class="slab-body--padded">
              {#if !streamMetadata}
                <div class="flex flex-col items-center justify-center py-8 text-center text-muted-foreground">
                  <LoadingSpinner class="w-6 h-6 mb-2 opacity-50" />
                  <span class="text-xs">Waiting for stream data...</span>
                </div>
              {:else}
                <div class="space-y-4">
                  <!-- Resolution & Codec -->
                  <div class="grid grid-cols-2 gap-4">
                    <div class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]">
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">Resolution</div>
                      <div class="font-mono text-sm text-foreground">
                        {resolutionLabel || 'Unknown'}
                      </div>
                    </div>
                    <div class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]">
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">Codec</div>
                      <div class="font-mono text-sm text-foreground uppercase">
                        {codecLabel || 'Unknown'}
                      </div>
                    </div>
                  </div>

                  <!-- FPS & Bitrate -->
                  <div class="grid grid-cols-2 gap-4">
                    <div class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]">
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">Frame Rate</div>
                      <div class="font-mono text-sm text-foreground">
                        {fpsLabel || 'Unknown'}
                      </div>
                    </div>
                    <div class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]">
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">Bitrate</div>
                      <div class="font-mono text-sm text-foreground">
                        {bitrateLabel || 'Unknown'}
                      </div>
                    </div>
                  </div>

                  <!-- Advanced Info -->
                  {#if streamMetadata.protocol || streamMetadata.nodeId || streamMetadata.geoDistance !== undefined}
                    <div class="pt-4 border-t border-[hsl(var(--tn-fg-gutter)/0.2)]">
                      <div class="flex justify-between items-center text-xs">
                        <span class="text-muted-foreground">Protocol</span>
                        <span class="font-mono text-foreground uppercase">{streamMetadata.protocol || 'N/A'}</span>
                      </div>
                      {#if streamMetadata.nodeId}
                        <div class="flex justify-between items-center text-xs mt-2">
                          <span class="text-muted-foreground">Node</span>
                          <span class="font-mono text-foreground">{streamMetadata.nodeId}</span>
                        </div>
                      {/if}
                      {#if streamMetadata.geoDistance !== undefined}
                        <div class="flex justify-between items-center text-xs mt-2">
                          <span class="text-muted-foreground">Geo Distance</span>
                          <span class="font-mono text-foreground">{streamMetadata.geoDistance.toFixed(0)} km</span>
                        </div>
                      {/if}
                    </div>
                  {/if}

                  <!-- Debug Info (Merged) -->
                  {#if playerConfig.options.devMode}
                    <div class="pt-4 border-t border-[hsl(var(--tn-fg-gutter)/0.2)]">
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-2">Debug Info</div>
                      <div class="text-xs font-mono text-muted-foreground break-all bg-[hsl(var(--tn-bg-dark)/0.5)] p-2 rounded">
                        <div>ID: {contentId}</div>
                        <div class="mt-1">Type: {contentType}</div>
                      </div>
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        </div>
      </div>
    </div>
  {/if}
</div>
