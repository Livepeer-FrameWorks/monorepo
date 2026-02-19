<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import {
    createStudioTranslator,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import CameraIcon from "../icons/CameraIcon.svelte";
  import MonitorIcon from "../icons/MonitorIcon.svelte";
  import MicIcon from "../icons/MicIcon.svelte";
  import XIcon from "../icons/XIcon.svelte";
  import VideoIcon from "../icons/VideoIcon.svelte";
  import ChevronsRightIcon from "../icons/ChevronsRightIcon.svelte";
  import ChevronsLeftIcon from "../icons/ChevronsLeftIcon.svelte";
  import VolumeSlider from "./VolumeSlider.svelte";
  import type { MediaSource } from "@livepeer-frameworks/streamcrafter-core";

  interface Props {
    sources?: MediaSource[];
    enableCompositor?: boolean;
    defaultCollapsed?: boolean;
  }

  let { sources: propSources, enableCompositor = true, defaultCollapsed = false }: Props = $props();

  let pc: any = getContext("fw-sc-controller");
  const translatorCtx = getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator");
  const fallbackT = createStudioTranslator({ locale: "en" });
  let t: StudioTranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);
  let showSources = $state(!defaultCollapsed);

  let sources = $derived(propSources ?? pc?.sources ?? []);
</script>

{#if sources.length > 0}
  <div class="fw-sc-section fw-sc-mixer {!showSources ? 'fw-sc-section--collapsed' : ''}">
    <div
      class="fw-sc-section-header"
      onclick={() => (showSources = !showSources)}
      role="button"
      tabindex="0"
      onkeydown={(e) => e.key === "Enter" && (showSources = !showSources)}
      title={showSources ? t("collapseMixer") : t("expandMixer")}
    >
      <span>{t("mixer")} ({sources.length})</span>
      {#if showSources}
        <ChevronsRightIcon size={14} />
      {:else}
        <ChevronsLeftIcon size={14} />
      {/if}
    </div>
    {#if showSources}
      <div class="fw-sc-sources">
        {#each sources as source (source.id)}
          <div class="fw-sc-source">
            <div class="fw-sc-source-icon">
              {#if source.type === "camera"}
                <CameraIcon size={16} />
              {:else if source.type === "screen"}
                <MonitorIcon size={16} />
              {/if}
            </div>
            <div class="fw-sc-source-info">
              <div class="fw-sc-source-label">
                {source.label}
                {#if source.primaryVideo && !enableCompositor}
                  <span class="fw-sc-primary-badge">{t("primary")}</span>
                {/if}
              </div>
              <div class="fw-sc-source-type">{source.type}</div>
            </div>
            <div class="fw-sc-source-controls">
              {#if source.stream.getVideoTracks().length > 0 && !enableCompositor}
                <button
                  type="button"
                  class="fw-sc-icon-btn {source.primaryVideo ? 'fw-sc-icon-btn--primary' : ''}"
                  onclick={() => pc?.setPrimaryVideoSource(source.id)}
                  disabled={source.primaryVideo}
                  title={source.primaryVideo ? t("primaryVideoSource") : t("setAsPrimary")}
                >
                  <VideoIcon size={14} active={source.primaryVideo} />
                </button>
              {/if}
              <span class="fw-sc-volume-label">{Math.round(source.volume * 100)}%</span>
              <VolumeSlider
                value={source.volume}
                onChange={(volume) => pc?.setSourceVolume(source.id, volume)}
                compact={true}
              />
              <button
                type="button"
                class="fw-sc-icon-btn {source.muted ? 'fw-sc-icon-btn--active' : ''}"
                onclick={() => pc?.setSourceMuted(source.id, !source.muted)}
                title={source.muted ? t("unmute") : t("mute")}
              >
                <MicIcon size={14} muted={source.muted} />
              </button>
              <button
                type="button"
                class="fw-sc-icon-btn fw-sc-icon-btn--destructive"
                onclick={() => pc?.removeSource(source.id)}
                disabled={pc?.isStreaming}
                title={pc?.isStreaming ? t("cannotRemoveWhileStreaming") : t("removeSource")}
              >
                <XIcon size={14} />
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
{/if}
