<script lang="ts">
  import { getContext } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    type MediaSource,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import CameraIcon from "../icons/CameraIcon.svelte";
  import CompositorControls from "./CompositorControls.svelte";
  import type { CompositorContext } from "../stores/compositor";

  interface Props {
    /** Show the layout toolbar over the preview while the compositor runs */
    enableCompositor?: boolean;
    /** Override sources (falls back to context) */
    sources?: MediaSource[];
  }

  let { enableCompositor = true, sources: sourcesProp }: Props = $props();

  let pc: any = getContext("fw-sc-controller");
  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let compositor = $derived((pc as CompositorContext | undefined)?.compositor ?? null);
  let composition = $derived((pc as CompositorContext | undefined)?.compositorState ?? null);
  let sources = $derived<MediaSource[]>(sourcesProp ?? pc?.sources ?? []);
  let activeLayers = $derived(
    composition?.scenes.find((scene) => scene.id === composition?.activeSceneId)?.layers ?? []
  );

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

    {#if enableCompositor && compositor && composition}
      <CompositorControls
        isEnabled={composition.isEnabled}
        isInitialized={composition.isInitialized}
        rendererType={composition.rendererType}
        stats={composition.stats}
        {sources}
        layers={activeLayers}
        currentLayout={composition.currentLayout}
        onLayoutApply={(layout) => compositor?.applyLayout(layout)}
        onCycleSourceOrder={(direction) => compositor?.cycleSourceOrder(direction)}
      />
    {/if}
  </div>
</div>
