<script lang="ts">
  import { getContext } from "svelte";
  import { readable } from "svelte/store";
  import type { Readable } from "svelte/store";
  import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
  import { PlayIcon, PauseIcon } from "../icons";

  let pc: any = getContext("fw-player-controller");
  const translatorStore: Readable<TranslateFn> =
    getContext<Readable<TranslateFn> | undefined>("fw-translator") ??
    readable(createTranslator({ locale: "en" }));
  let t: TranslateFn = $derived($translatorStore);
</script>

<button
  type="button"
  class="fw-btn-flush"
  aria-label={pc?.isPlaying ? t("pause") : t("play")}
  aria-pressed={pc?.isPlaying ?? false}
  title={pc?.isPlaying ? t("pause") : t("play")}
  onclick={() => pc?.togglePlay()}
>
  {#if pc?.isPlaying}
    <PauseIcon size={18} />
  {:else}
    <PlayIcon size={18} />
  {/if}
</button>
