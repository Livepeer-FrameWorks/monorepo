<script lang="ts">
  import { onDestroy } from "svelte";
  import { getSpriteCues } from "$lib/utils/spriteCache";
  import type { ThumbnailCue } from "@livepeer-frameworks/player-core";
  import { getIconComponent } from "$lib/iconUtils";

  interface Props {
    assetId: string | null;
    posterUrl?: string | null;
    width?: number;
    height?: number;
  }

  let { assetId = null, posterUrl = null, width = 64, height = 36 }: Props = $props();

  const VideoIcon = getIconComponent("Video");

  let cues = $state<ThumbnailCue[]>([]);
  let spriteUrl = $state("");
  let frameIndex = $state(0);
  let animating = $state(false);
  let hovering = $state(false);
  let intervalId: ReturnType<typeof setInterval> | null = null;
  let spriteLoaded = $state(false);
  let posterError = $state(false);

  $effect(() => {
    if (posterUrl !== undefined) {
      posterError = false;
    }
  });

  function startAnimation() {
    hovering = true;
    if (!assetId) return;
    getSpriteCues(assetId).then((data) => {
      if (!data || !hovering) return;
      cues = data.cues;
      spriteUrl = data.spriteUrl;
      const img = new Image();
      img.onload = () => {
        if (!hovering) return;
        spriteLoaded = true;
        animating = true;
        frameIndex = 0;
        intervalId = setInterval(() => {
          frameIndex = (frameIndex + 1) % cues.length;
        }, 500);
      };
      img.src = data.spriteUrl;
    });
  }

  function stopAnimation() {
    hovering = false;
    animating = false;
    spriteLoaded = false;
    if (intervalId) {
      clearInterval(intervalId);
      intervalId = null;
    }
  }

  onDestroy(stopAnimation);

  function getBackgroundPosition(cue: ThumbnailCue): string {
    if (cue.x == null || cue.y == null || cue.width == null || cue.height == null) return "0 0";
    const scaleX = width / cue.width;
    const scaleY = height / cue.height;
    return `-${cue.x * scaleX}px -${cue.y * scaleY}px`;
  }

  function getBackgroundSize(): string {
    return `${width * 10}px ${height * 10}px`;
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="rounded bg-muted shrink-0 overflow-hidden"
  style="width: {width}px; height: {height}px;"
  onmouseenter={startAnimation}
  onmouseleave={stopAnimation}
>
  {#if animating && spriteLoaded && cues[frameIndex]}
    <div
      class="w-full h-full"
      style="background-image: url({spriteUrl}); background-position: {getBackgroundPosition(
        cues[frameIndex]
      )}; background-size: {getBackgroundSize()}; background-repeat: no-repeat;"
    ></div>
  {:else if posterUrl && !posterError}
    <img
      src={posterUrl}
      alt=""
      loading="lazy"
      class="w-full h-full object-cover"
      onerror={() => {
        posterError = true;
      }}
    />
  {:else}
    <div class="w-full h-full flex items-center justify-center">
      <VideoIcon class="w-4 h-4 text-muted-foreground/50" />
    </div>
  {/if}
</div>
