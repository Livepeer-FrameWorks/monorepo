<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import {
    createStudioTranslator,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import CameraIcon from "../icons/CameraIcon.svelte";

  let pc: any = getContext("fw-sc-controller");
  const translatorCtx = getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator");
  const fallbackT = createStudioTranslator({ locale: "en" });
  let t: StudioTranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);

  let videoEl: HTMLVideoElement;

  $effect(() => {
    if (videoEl && pc?.mediaStream) {
      videoEl.srcObject = pc.mediaStream;
      videoEl.play().catch(() => {});
    } else if (videoEl) {
      videoEl.srcObject = null;
    }
  });

  let statusText = $derived.by(() => {
    if (pc?.state === "connecting") return t("connecting");
    if (pc?.state === "reconnecting") return t("reconnecting");
    return "";
  });
</script>

<div class="fw-sc-preview-wrapper">
  <div class="fw-sc-preview">
    <video bind:this={videoEl} playsinline muted autoplay aria-label={t("streamPreview")}></video>

    {#if !pc?.mediaStream}
      <div class="fw-sc-preview-placeholder">
        <CameraIcon size={48} />
        <span>{t("addSourcePrompt")}</span>
      </div>
    {/if}

    {#if pc?.state === "connecting" || pc?.state === "reconnecting"}
      <div class="fw-sc-status-overlay">
        <div class="fw-sc-status-spinner"></div>
        <span class="fw-sc-status-text">{statusText}</span>
      </div>
    {/if}

    {#if pc?.isStreaming}
      <div class="fw-sc-live-badge">{t("live")}</div>
    {/if}
  </div>
</div>
