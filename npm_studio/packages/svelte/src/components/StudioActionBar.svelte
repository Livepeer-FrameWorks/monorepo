<script lang="ts">
  import { getContext } from "svelte";
  import type { Snippet } from "svelte";
  import type { Readable } from "svelte/store";
  import {
    createStudioTranslator,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import CameraIcon from "../icons/CameraIcon.svelte";
  import MonitorIcon from "../icons/MonitorIcon.svelte";
  import StudioSettings from "./StudioSettings.svelte";

  interface Props {
    whipUrl?: string;
    showSettingsButton?: boolean;
    children?: Snippet;
  }

  let { whipUrl, showSettingsButton = true, children }: Props = $props();

  let pc: any = getContext("fw-sc-controller");
  const translatorCtx = getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator");
  const fallbackT = createStudioTranslator({ locale: "en" });
  let t: StudioTranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);

  let canAddSource = $derived(pc?.state !== "destroyed" && pc?.state !== "error");
  let hasCamera = $derived(pc?.sources?.some((s: any) => s.type === "camera") ?? false);
  let canStream = $derived(pc?.isCapturing && !pc?.isStreaming && whipUrl !== undefined);

  async function handleStartCamera() {
    try {
      await pc?.startCamera();
    } catch (err) {
      console.error("Failed to start camera:", err);
    }
  }

  async function handleStartScreenShare() {
    try {
      await pc?.startScreenShare({ audio: true });
    } catch (err) {
      console.error("Failed to start screen share:", err);
    }
  }

  async function handleGoLive() {
    try {
      await pc?.startStreaming();
    } catch (err) {
      console.error("Failed to start streaming:", err);
    }
  }

  async function handleStop() {
    await pc?.stopStreaming();
  }
</script>

<div class="fw-sc-actions">
  {#if children}
    {@render children()}
  {:else}
    <button
      type="button"
      class="fw-sc-action-secondary"
      onclick={handleStartCamera}
      disabled={!canAddSource || hasCamera}
      title={hasCamera ? t("cameraActive") : t("addCamera")}
    >
      <CameraIcon size={18} />
    </button>
    <button
      type="button"
      class="fw-sc-action-secondary"
      onclick={handleStartScreenShare}
      disabled={!canAddSource}
      title={t("shareScreen")}
    >
      <MonitorIcon size={18} />
    </button>
    {#if showSettingsButton}
      <StudioSettings />
    {/if}
    {#if !pc?.isStreaming}
      <button
        type="button"
        class="fw-sc-action-primary"
        onclick={handleGoLive}
        disabled={!canStream}
      >
        {pc?.state === "connecting" ? t("connecting") : t("goLive")}
      </button>
    {:else}
      <button type="button" class="fw-sc-action-primary fw-sc-action-stop" onclick={handleStop}>
        {t("stopStreaming")}
      </button>
    {/if}
  {/if}
</div>
