<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
  import { SkipBackIcon, SkipForwardIcon } from "../icons";

  interface Props {
    direction: "back" | "forward";
    seconds?: number;
  }

  let { direction, seconds = 10 }: Props = $props();
  let pc: any = getContext("fw-player-controller");
  const translatorCtx = getContext<Readable<TranslateFn> | undefined>("fw-translator");
  const fallbackT = createTranslator({ locale: "en" });
  let t: TranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);

  let label = $derived(direction === "back" ? t("skipBackward") : t("skipForward"));
</script>

<button
  type="button"
  class="fw-btn-flush"
  aria-label={label}
  title={label}
  onclick={() => pc?.seek((pc?.currentTime ?? 0) + (direction === "back" ? -seconds : seconds))}
>
  {#if direction === "back"}
    <SkipBackIcon size={16} />
  {:else}
    <SkipForwardIcon size={16} />
  {/if}
</button>
