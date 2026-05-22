<!--
  LoadingPoster.svelte - Loading-state poster overlay.

  Dumb renderer: dispatches on `loadingPoster.mode` and reads spec fields. The
  controller (PlayerController.buildLoadingPosterInfo) owns source priority
  and the synthetic-vs-measured decision.
-->
<script lang="ts" context="module">
  import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

  const CYCLE_MS = 2000;
  const CROP_INSET_PX = 0.5;
  const animationStartTimes = new Map<string, number>();
  const completedAnimations = new Set<string>();

  function animationKeyFor(p: LoadingPosterInfo | null): string | null {
    if (!p || p.mode !== "animate" || p.geometry !== "measured" || !p.spriteJpgUrl) return null;
    if (p.cues.length < 2) return null;
    const first = p.cues[0];
    const last = p.cues[p.cues.length - 1];
    return [
      p.prerollKey ?? p.staticUrl ?? p.spriteJpgUrl,
      p.cues.length,
      first.x,
      first.y,
      first.width,
      first.height,
      last.x,
      last.y,
      last.width,
      last.height,
    ].join("|");
  }

  function cueIndexFor(tickIdx: number, cueCount: number): number {
    return Math.max(0, Math.min(tickIdx, cueCount - 1));
  }
</script>

<script lang="ts">
  import { onDestroy } from "svelte";

  interface Props {
    loadingPoster: LoadingPosterInfo | null;
    className?: string;
  }

  let { loadingPoster = null, className = "" }: Props = $props();

  const clipId = `fw-loading-poster-clip-${Math.random().toString(36).slice(2)}`;

  let tickIdx = $state(0);
  let intervalId: ReturnType<typeof setInterval> | undefined;
  let measuredUrl = $state<string | null>(null);
  let measuredW = $state(0);
  let measuredH = $state(0);
  let spriteFailed = $state(false);
  let animationCompleted = $state(false);

  let isAnimate = $derived(loadingPoster?.mode === "animate");
  let animationKey = $derived(animationKeyFor(loadingPoster));

  $effect(() => {
    const tiles =
      loadingPoster?.mode === "animate" && loadingPoster.geometry === "measured"
        ? loadingPoster.cues.length
        : 0;
    if (!animationKey || tiles < 2) {
      tickIdx = 0;
      animationCompleted = false;
      if (intervalId !== undefined) {
        clearInterval(intervalId);
        intervalId = undefined;
      }
      return;
    }

    if (completedAnimations.has(animationKey)) {
      animationCompleted = true;
      tickIdx = tiles - 1;
      if (intervalId !== undefined) {
        clearInterval(intervalId);
        intervalId = undefined;
      }
      return;
    }

    const stepMs = Math.max(20, Math.floor(CYCLE_MS / tiles));
    const now = Date.now();
    const existingStart = animationStartTimes.get(animationKey);
    const startedAt = existingStart !== undefined && existingStart <= now ? existingStart : now;
    animationStartTimes.set(animationKey, startedAt);

    const updateFrame = () => {
      const elapsed = Date.now() - startedAt;
      const current = Math.min(Math.floor(elapsed / stepMs), tiles - 1);
      tickIdx = current;
      if (current >= tiles - 1) {
        completedAnimations.add(animationKey);
        animationCompleted = true;
        if (intervalId !== undefined) {
          clearInterval(intervalId);
          intervalId = undefined;
        }
      }
    };

    animationCompleted = false;
    if (intervalId !== undefined) clearInterval(intervalId);
    updateFrame();
    if (!animationCompleted) intervalId = setInterval(updateFrame, stepMs);
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
      const cue = loadingPoster.cues[cueIndexFor(tickIdx, loadingPoster.cues.length)];
      if (!cue) return null;
      if (measuredUrl !== loadingPoster.spriteJpgUrl || measuredW <= 0 || measuredH <= 0) {
        return null;
      }
      const inset = Math.min(CROP_INSET_PX, cue.width / 4, cue.height / 4);
      return {
        x: cue.x + inset,
        y: cue.y + inset,
        viewW: Math.max(1, cue.width - inset * 2),
        viewH: Math.max(1, cue.height - inset * 2),
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
    {#if cueRect && loadingPoster.spriteJpgUrl && !(animationCompleted && staticSrc)}
      <svg
        class="fw-loading-poster-sprite"
        viewBox={`0 0 ${cueRect.viewW} ${cueRect.viewH}`}
        preserveAspectRatio="xMidYMid meet"
      >
        <defs>
          <clipPath id={clipId} clipPathUnits="userSpaceOnUse">
            <rect x="0" y="0" width={cueRect.viewW} height={cueRect.viewH} />
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
