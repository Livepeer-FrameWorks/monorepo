<!--
  LoadingPoster.svelte - Loading-state poster overlay.

  Dumb renderer: dispatches on `loadingPoster.mode` and reads spec fields. The
  controller (PlayerController.buildLoadingPosterInfo) owns source priority
  and the synthetic-vs-measured decision.
-->
<script lang="ts">
  import { onDestroy } from "svelte";
  import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

  interface Props {
    loadingPoster: LoadingPosterInfo | null;
    className?: string;
  }

  let { loadingPoster = null, className = "" }: Props = $props();

  const CYCLE_MS = 2000;
  const clipId = `fw-loading-poster-clip-${Math.random().toString(36).slice(2)}`;

  let tickIdx = $state(0);
  let intervalId: ReturnType<typeof setInterval> | undefined;
  let measuredUrl = $state<string | null>(null);
  let measuredW = $state(0);
  let measuredH = $state(0);
  let spriteFailed = $state(false);

  let isAnimate = $derived(loadingPoster?.mode === "animate");
  let cueCount = $derived(loadingPoster?.cues.length ?? 0);
  let tileCount = $derived(isAnimate && loadingPoster?.geometry === "measured" ? cueCount : 0);

  $effect(() => {
    if (!isAnimate || tileCount < 2) {
      tickIdx = 0;
      if (intervalId !== undefined) {
        clearInterval(intervalId);
        intervalId = undefined;
      }
      return;
    }
    const stepMs = Math.max(20, Math.floor(CYCLE_MS / tileCount));
    if (intervalId !== undefined) clearInterval(intervalId);
    intervalId = setInterval(() => {
      tickIdx = (tickIdx + 1) % tileCount;
    }, stepMs);
    return () => {
      if (intervalId !== undefined) clearInterval(intervalId);
      intervalId = undefined;
    };
  });

  $effect(() => {
    if (!isAnimate || loadingPoster?.geometry !== "measured") return;
    const url = loadingPoster?.spriteJpgUrl;
    if (!url) return;
    if (measuredUrl === url && (measuredW > 0 || spriteFailed)) return;
    measuredUrl = url;
    measuredW = 0;
    measuredH = 0;
    spriteFailed = false;
    const img = new Image();
    let cancelled = false;
    img.onload = () => {
      if (cancelled) return;
      measuredW = img.naturalWidth;
      measuredH = img.naturalHeight;
    };
    img.onerror = () => {
      if (cancelled) return;
      spriteFailed = true;
    };
    img.src = url;
    return () => {
      cancelled = true;
    };
  });

  onDestroy(() => {
    if (intervalId !== undefined) clearInterval(intervalId);
  });

  function shouldCacheBust(p: LoadingPosterInfo): boolean {
    if (!p.staticUrl) return false;
    if (p.staticUrl.startsWith("data:") || p.staticUrl.startsWith("blob:")) return false;
    if (p.staticSource === "thumbnail-prop") return false;
    return true;
  }
  function withCacheBust(p: LoadingPosterInfo): string | undefined {
    if (!p.staticUrl) return undefined;
    if (!shouldCacheBust(p)) return p.staticUrl;
    const sep = p.staticUrl.includes("?") ? "&" : "?";
    return `${p.staticUrl}${sep}_g=${p.generation}`;
  }

  // Resolve current cue rect for animate mode.
  let cueRect = $derived.by(() => {
    if (!isAnimate || !loadingPoster) return null;
    if (loadingPoster.geometry === "measured") {
      const cue = loadingPoster.cues[tickIdx % Math.max(loadingPoster.cues.length, 1)];
      if (!cue) return null;
      if (measuredUrl !== loadingPoster.spriteJpgUrl || measuredW <= 0 || measuredH <= 0) {
        return null;
      }
      return {
        x: cue.x,
        y: cue.y,
        w: cue.width,
        h: cue.height,
        imgW: measuredW,
        imgH: measuredH,
      };
    }
    return null;
  });

  let staticSrc = $derived(loadingPoster ? withCacheBust(loadingPoster) : undefined);
</script>

{#if loadingPoster}
  <div class={`fw-loading-poster-root ${className}`} aria-hidden="true">
    {#if cueRect && loadingPoster.spriteJpgUrl}
      <svg
        class="fw-loading-poster-sprite"
        viewBox={`0 0 ${cueRect.w} ${cueRect.h}`}
        preserveAspectRatio="xMidYMid meet"
      >
        <defs>
          <clipPath id={clipId} clipPathUnits="userSpaceOnUse">
            <rect x="0" y="0" width={cueRect.w} height={cueRect.h} />
          </clipPath>
        </defs>
        <g clip-path={`url(#${clipId})`}>
          <image
            href={loadingPoster.spriteJpgUrl}
            x={-cueRect.x}
            y={-cueRect.y}
            width={cueRect.imgW}
            height={cueRect.imgH}
            preserveAspectRatio="none"
          />
        </g>
      </svg>
    {:else if staticSrc}
      <img class="fw-loading-poster-img" src={staticSrc} alt="" />
    {/if}
  </div>
{/if}

<style>
  .fw-loading-poster-root {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    background: #000;
    overflow: hidden;
    pointer-events: none;
  }
  .fw-loading-poster-sprite {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    overflow: hidden;
  }
  .fw-loading-poster-img {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    background: #000;
    object-fit: contain;
  }
</style>
