<!--
  ThumbnailOverlay.svelte - Pre-play poster with play button
  Port of src/components/ThumbnailOverlay.tsx
-->
<script lang="ts">
  import { cn } from "@livepeer-frameworks/player-core";

  interface Props {
    thumbnailUrl?: string | null;
    onPlay?: () => void;
    message?: string | null;
    showUnmuteMessage?: boolean;
    style?: string;
    class?: string;
  }

  let {
    thumbnailUrl = null,
    onPlay = undefined,
    message = null,
    showUnmuteMessage = false,
    style = "",
    class: className = "",
  }: Props = $props();

  function handleClick() {
    onPlay?.();
  }

  function handleKeyDown(event: KeyboardEvent) {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      handleClick();
    }
  }
</script>

<div
  role="button"
  tabindex="0"
  onclick={handleClick}
  onkeydown={handleKeyDown}
  {style}
  class={cn(
    "fw-player-thumbnail relative flex h-full min-h-[280px] w-full cursor-pointer items-center justify-center overflow-hidden rounded-xl bg-slate-950 text-foreground outline-none transition focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    className
  )}
>
  {#if thumbnailUrl}
    <div
      class="absolute inset-0 bg-cover bg-center"
      style="background-image: url({thumbnailUrl})"
    ></div>
  {/if}

  <div
    class={cn(
      "absolute inset-0 bg-slate-950/70",
      !thumbnailUrl && "bg-gradient-to-br from-slate-900 via-slate-950 to-slate-900"
    )}
  ></div>

  <div
    class="relative z-10 flex max-w-[320px] flex-col items-center gap-4 px-6 text-center text-sm sm:gap-6"
  >
    {#if showUnmuteMessage}
      <div
        class="w-full rounded-lg border border-white/15 bg-black/80 p-4 text-sm text-white shadow-lg backdrop-blur"
      >
        <div
          class="mb-1 flex items-center justify-center gap-2 text-base font-semibold text-primary"
        >
          <span aria-hidden="true">🔇</span> Click to unmute
        </div>
        <p class="text-xs text-white/80">Stream is playing muted — tap to enable sound.</p>
      </div>
    {:else}
      <button
        type="button"
        class="h-20 w-20 rounded-full bg-primary/90 text-primary-foreground shadow-lg shadow-primary/40 transition hover:bg-primary focus-visible:bg-primary flex items-center justify-center"
        aria-label="Play stream"
      >
        <svg viewBox="0 0 24 24" fill="currentColor" class="ml-0.5 h-8 w-8" aria-hidden="true">
          <path d="M8 5v14l11-7z" />
        </svg>
      </button>
      <div
        class="w-full rounded-lg border border-white/10 bg-black/70 p-5 text-white shadow-inner backdrop-blur"
      >
        <p class="text-base font-semibold text-primary">
          {message ?? "Click to play"}
        </p>
        <p class="mt-1 text-xs text-white/70">
          {message ? "Start streaming instantly" : "Jump into the live feed"}
        </p>
      </div>
    {/if}
  </div>
</div>
