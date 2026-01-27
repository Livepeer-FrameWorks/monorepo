<script lang="ts">
  import { Player } from "@livepeer-frameworks/player-svelte";
  import type {
    PlayerState,
    PlayerStateContext,
    PlayerOptions as CorePlayerOptions,
    PlayerMetadata,
  } from "@livepeer-frameworks/player-svelte";
  import { getGraphqlHttpUrl } from "$lib/config";

  interface Props {
    contentId: string;
    contentType?: "live" | "clip" | "dvr" | "vod";
    thumbnailUrl?: string | null;
    options?: Partial<CorePlayerOptions>;
    onStateChange?: (state: PlayerState, context?: PlayerStateContext) => void;
    onMetadata?: (metadata: PlayerMetadata) => void;
  }

  let {
    contentId,
    contentType,
    thumbnailUrl = null,
    options = {},
    onStateChange,
    onMetadata,
  }: Props = $props();

  // Resolve gateway URL with dev environment handling
  const resolveGatewayUrl = (url?: string) => {
    const fallbackUrl = getGraphqlHttpUrl();
    if (!url) return fallbackUrl;

    // In dev, if using relative path, force Nginx port
    if (import.meta.env.DEV && url.startsWith("/")) {
      if (!fallbackUrl) return url;
      try {
        const base = new URL(fallbackUrl);
        return `${base.protocol}//${base.host}${url}`;
      } catch {
        return url;
      }
    }

    return url;
  };

  // Build options object for the underlying Player component
  const playerOptions = $derived({
    gatewayUrl: resolveGatewayUrl(options.gatewayUrl ?? import.meta.env.VITE_GRAPHQL_HTTP_URL),
    authToken: options.authToken,
    autoplay: options.autoplay ?? true,
    muted: options.muted ?? true,
    controls: options.controls ?? true,
    debug: options.debug ?? false,
    devMode: options.devMode ?? false,
    stockControls: options.stockControls,
    forcePlayer: options.forcePlayer,
    forceType: options.forceType,
    playbackMode: options.playbackMode,
    mistUrl: options.mistUrl,
  });
</script>

<div class="player-wrapper w-full h-full relative bg-black overflow-hidden">
  <Player
    {contentId}
    {contentType}
    {thumbnailUrl}
    options={playerOptions}
    {onStateChange}
    {onMetadata}
  />
</div>

<style>
  .player-wrapper {
    background: #000;
  }
</style>
