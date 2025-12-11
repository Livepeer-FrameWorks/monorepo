<script lang="ts">
  import { onMount, onDestroy } from "svelte";

  let playerModulePromise: Promise<typeof import("@livepeer-frameworks/player")> | null =
    typeof window !== "undefined" ? import("@livepeer-frameworks/player") : null;

  interface Props {
    contentId: string;
    contentType: "live" | "clip" | "dvr";
    thumbnailUrl?: string | null;
    options?: PlayerOptions;
  }

  interface PlayerOptions {
    gatewayUrl?: string;
    autoplay?: boolean;
    muted?: boolean;
    controls?: boolean;
    debug?: boolean;
    authToken?: string;
  }

  let {
    contentId,
    contentType,
    thumbnailUrl = null,
    options = {},
  }: Props = $props();

  let playerContainer = $state<HTMLDivElement | null>(null);
  let player: { destroy?: () => void } | null = null;
  let loading = $state(true);
  let error = $state("");

  // Default options
  const defaultOptions = {
    gatewayUrl: import.meta.env.VITE_GRAPHQL_HTTP_URL,
    autoplay: true,
    muted: true,
    controls: true,
    debug: false,
  };

  // Merge options
  const playerOptions = { ...defaultOptions, ...options };

  // Get auth token if available
  if (typeof window !== "undefined") {
    const token = localStorage.getItem("token");
    if (token) {
      playerOptions.authToken = token;
    }
  }

  onMount(async () => {
    try {
      if (!playerModulePromise) {
        playerModulePromise = import("@livepeer-frameworks/player");
      }

      // Type assertion needed because npm_player type exports are currently empty
      const playerModule = await playerModulePromise as { Player: new (container: HTMLDivElement | null, config: unknown) => { destroy?: () => void } };
      const { Player: FrameWorksPlayer } = playerModule;

      // Initialize the NPM player - it handles everything internally
      player = new FrameWorksPlayer(playerContainer, {
        contentId,
        contentType,
        thumbnailUrl,
        options: playerOptions,
      });

      loading = false;
    } catch (err) {
      console.error("Failed to initialize player:", err);
      const message =
        err instanceof Error
          ? err.message
          : typeof err === "string"
            ? err
            : "Failed to load player";
      error = message;
      loading = false;
    }
  });

  onDestroy(() => {
    player?.destroy?.();
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
          <svg class="w-16 h-16 mx-auto text-error" fill="currentColor" viewBox="0 0 20 20">
            <path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 102 0V6a1 1 0 00-1-1z" clip-rule="evenodd" />
          </svg>
        </div>
        <h3 class="text-lg font-semibold mb-2">Player Error</h3>
        <p class="text-sm text-muted-foreground">{error}</p>
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
