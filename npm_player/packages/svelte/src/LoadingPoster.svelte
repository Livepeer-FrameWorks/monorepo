<!--
  LoadingPoster.svelte - Loading-state poster overlay.

  Source priority (per mode):
    - "animate": sprite cycle → Chandler poster.jpg → Mist preview JPEG → fallbackPosterUrl
    - "latest":  Chandler poster.jpg → Mist preview JPEG → fallbackPosterUrl (never uses sprite)
-->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

  type Mode = "animate" | "latest";

  interface Props {
    loadingPoster: LoadingPosterInfo | null;
    mode?: Mode;
    fallbackPosterUrl?: string;
    cycleMs?: number;
  }

  let {
    loadingPoster = null,
    mode = "animate",
    fallbackPosterUrl = undefined,
    cycleMs = 2000,
  }: Props = $props();

  let tickIdx = $state(0);
  let intervalId: ReturnType<typeof setInterval> | undefined;

  let cues = $derived(loadingPoster?.cues ?? []);
  let spriteJpgUrl = $derived(loadingPoster?.spriteJpgUrl);
  let cols = $derived(loadingPoster?.columns ?? 0);
  let rows = $derived(loadingPoster?.rows ?? 0);
  let spriteWidth = $derived(loadingPoster?.spriteWidth ?? 0);
  let spriteHeight = $derived(loadingPoster?.spriteHeight ?? 0);
  let canAnimateSprite = $derived(
    mode === "animate" && !!spriteJpgUrl && cues.length >= 2 && cols > 0 && rows > 0
  );

  $effect(() => {
    if (!canAnimateSprite) {
      tickIdx = 0;
      if (intervalId !== undefined) {
        clearInterval(intervalId);
        intervalId = undefined;
      }
      return;
    }
    const stepMs = Math.max(20, Math.floor(cycleMs / cues.length));
    if (intervalId !== undefined) clearInterval(intervalId);
    intervalId = setInterval(() => {
      tickIdx = (tickIdx + 1) % cues.length;
    }, stepMs);
    return () => {
      if (intervalId !== undefined) clearInterval(intervalId);
      intervalId = undefined;
    };
  });

  onDestroy(() => {
    if (intervalId !== undefined) clearInterval(intervalId);
  });

  let cue = $derived(canAnimateSprite ? cues[tickIdx % cues.length] : undefined);
  let viewBox = $derived(cue ? `${cue.x} ${cue.y} ${cue.width} ${cue.height}` : "0 0 1 1");
  let imageWidth = $derived(cue ? spriteWidth || cue.width * cols : 1);
  let imageHeight = $derived(cue ? spriteHeight || cue.height * rows : 1);

  let rawStaticUrl = $derived(
    !canAnimateSprite
      ? loadingPoster?.posterUrl || loadingPoster?.mistPreviewUrl || fallbackPosterUrl
      : undefined
  );
  let staticUrl = $derived.by(() => {
    if (!rawStaticUrl) return undefined;
    const isRefreshable =
      rawStaticUrl !== fallbackPosterUrl &&
      !rawStaticUrl.startsWith("data:") &&
      !rawStaticUrl.startsWith("blob:");
    if (!isRefreshable || !loadingPoster) return rawStaticUrl;
    const sep = rawStaticUrl.includes("?") ? "&" : "?";
    return `${rawStaticUrl}${sep}_g=${loadingPoster.generation}`;
  });
</script>

{#if canAnimateSprite}
  <svg
    class="fw-loading-poster-sprite"
    {viewBox}
    preserveAspectRatio="xMidYMid slice"
    aria-hidden="true"
  >
    <image href={spriteJpgUrl} x="0" y="0" width={imageWidth} height={imageHeight} />
  </svg>
{:else if staticUrl}
  <img class="fw-loading-poster-img" src={staticUrl} alt="" aria-hidden="true" />
{/if}

<style>
  .fw-loading-poster-sprite {
    position: absolute;
    inset: 0;
    pointer-events: none;
  }
  .fw-loading-poster-img {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    object-fit: cover;
    pointer-events: none;
  }
</style>
