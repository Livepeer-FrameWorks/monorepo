<script lang="ts">
  import { getContext } from "svelte";
  import { readable } from "svelte/store";
  import type { Readable } from "svelte/store";
  import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
  import { SeekToLiveIcon } from "../icons";

  let pc: any = getContext("fw-player-controller");
  const translatorStore: Readable<TranslateFn> =
    getContext<Readable<TranslateFn> | undefined>("fw-translator") ??
    readable(createTranslator({ locale: "en" }));
  let t: TranslateFn = $derived($translatorStore);
</script>

{#if pc?.isEffectivelyLive}
  <button
    type="button"
    class="fw-live-badge fw-live-badge--active"
    onclick={() => pc?.jumpToLive()}
    aria-label={t("live")}
  >
    {t("live").toUpperCase()}
    <SeekToLiveIcon size={10} />
  </button>
{/if}
