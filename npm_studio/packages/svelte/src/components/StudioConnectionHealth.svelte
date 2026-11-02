<!--
  A pill that grades the publish connection while live, with the send rate,
  packet loss and round-trip time as its title. Reads stats from the
  StreamCrafter context unless they are given.
-->
<script lang="ts">
  import { getContext } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    assessConnectionHealth,
    createStudioTranslator,
    type IngestStats,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";

  interface Props {
    stats?: IngestStats | null;
    isStreaming?: boolean;
  }

  let { stats: statsProp, isStreaming: streamingProp }: Props = $props();

  const LABEL_KEYS = {
    good: "connectionGood",
    fair: "connectionFair",
    poor: "connectionPoor",
  } as const;

  const pc = getContext<{ stats: IngestStats | null; isStreaming: boolean } | undefined>(
    "fw-sc-controller"
  );
  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let isStreaming = $derived(streamingProp ?? pc?.isStreaming ?? false);
  let health = $derived(assessConnectionHealth(statsProp ?? pc?.stats ?? null));
</script>

{#if isStreaming && health}
  <span
    class="fw-sc-health fw-sc-health--{health.level}"
    role="status"
    title={t("connectionDetails", {
      bitrate: health.bitrateKbps,
      loss: health.packetLossPct.toFixed(1),
      rtt: Math.round(health.rttMs),
    })}
  >
    {t(LABEL_KEYS[health.level])}
  </span>
{/if}
