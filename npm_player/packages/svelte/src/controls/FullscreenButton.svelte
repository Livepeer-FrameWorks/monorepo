<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
  import { FullscreenIcon, FullscreenExitIcon } from "../icons";

  let pc: any = getContext("fw-player-controller");
  const translatorCtx = getContext<Readable<TranslateFn> | undefined>("fw-translator");
  const fallbackT = createTranslator({ locale: "en" });
  let t: TranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);
</script>

<button
  type="button"
  class="fw-btn-flush"
  aria-label={pc?.isFullscreen ? t("exitFullscreen") : t("fullscreen")}
  aria-pressed={pc?.isFullscreen ?? false}
  title={pc?.isFullscreen ? t("exitFullscreen") : t("fullscreen")}
  onclick={() => pc?.toggleFullscreen()}
>
  {#if pc?.isFullscreen}
    <FullscreenExitIcon size={16} />
  {:else}
    <FullscreenIcon size={16} />
  {/if}
</button>
