<script lang="ts">
  import { getContext, onMount } from "svelte";
  import type { Snippet } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    detectStudioSupport,
    FULL_STUDIO_SUPPORT,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import MonitorIcon from "../icons/MonitorIcon.svelte";
  import CameraPicker from "./CameraPicker.svelte";
  import MediaFileButton from "./MediaFileButton.svelte";
  import StudioSettings from "./StudioSettings.svelte";

  interface Props {
    whipUrl?: string;
    showSettingsButton?: boolean;
    children?: Snippet;
  }

  let { whipUrl, showSettingsButton = true, children }: Props = $props();

  let pc: any = getContext("fw-sc-controller");
  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let support = $state(FULL_STUDIO_SUPPORT);
  onMount(() => {
    support = detectStudioSupport();
  });

  let canAddSource = $derived(pc?.state !== "destroyed");
  let canStream = $derived(
    pc?.isCapturing && !pc?.isStreaming && whipUrl !== undefined && support.publish
  );

  async function handleStartCamera(videoDeviceId?: string) {
    try {
      await pc?.startCamera(videoDeviceId ? { videoDeviceId } : undefined);
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
    <CameraPicker
      sources={pc?.sources ?? []}
      disabled={!canAddSource || !support.camera}
      getChoices={() => pc?.getCameraChoices() ?? Promise.resolve([])}
      onPick={handleStartCamera}
    />
    <button
      type="button"
      class="fw-sc-action-secondary"
      onclick={handleStartScreenShare}
      disabled={!canAddSource || !support.screen}
      title={support.screen ? t("shareScreen") : t("screenShareUnsupported")}
    >
      <MonitorIcon size={18} />
    </button>
    <MediaFileButton
      onAdd={(file) => pc?.addMediaFile(file) ?? Promise.reject(new Error("No studio"))}
      disabled={!canAddSource}
    />
    {#if showSettingsButton}
      <StudioSettings />
    {/if}
    {#if !pc?.isStreaming && !pc?.isReconnecting}
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
