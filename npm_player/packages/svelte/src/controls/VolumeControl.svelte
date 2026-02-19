<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
  import { VolumeUpIcon, VolumeOffIcon } from "../icons";

  let pc: any = getContext("fw-player-controller");
  const translatorCtx = getContext<Readable<TranslateFn> | undefined>("fw-translator");
  const fallbackT = createTranslator({ locale: "en" });
  let t: TranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);
</script>

<button
  type="button"
  class="fw-btn-flush"
  aria-label={pc?.isMuted ? t("unmute") : t("mute")}
  aria-pressed={pc?.isMuted ?? false}
  title={pc?.isMuted ? t("unmute") : t("mute")}
  onclick={() => pc?.toggleMute()}
>
  {#if pc?.isMuted}
    <VolumeOffIcon size={16} />
  {:else}
    <VolumeUpIcon size={16} />
  {/if}
</button>
