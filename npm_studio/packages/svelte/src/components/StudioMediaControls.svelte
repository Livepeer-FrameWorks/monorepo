<!--
  Local media files in the mix, with their playback controls. Volume, mute and
  removal live with every other source in the mixer.
-->
<script lang="ts">
  import { getContext, onMount } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    detectStudioSupport,
    FULL_STUDIO_SUPPORT,
    type MediaSource,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";

  interface Props {
    sources: MediaSource[];
    addMediaFile: (file: File) => Promise<unknown>;
    playMediaSource: (sourceId: string) => void;
    pauseMediaSource: (sourceId: string) => void;
    setMediaSourceLoop: (sourceId: string, loop: boolean) => void;
  }

  let { sources, addMediaFile, playMediaSource, pauseMediaSource, setMediaSourceLoop }: Props =
    $props();

  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let support = $state(FULL_STUDIO_SUPPORT);
  onMount(() => {
    support = detectStudioSupport();
  });

  let fileFailed = $state(false);
  let files = $derived(sources.filter((source) => source.media));

  async function handleFile(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;

    fileFailed = false;
    try {
      await addMediaFile(file);
    } catch (err) {
      console.error("Failed to add media file:", err);
      fileFailed = true;
    }
  }
</script>

<div class="fw-sc-producer-section">
  <div class="fw-sc-producer-section-header">
    <span class="fw-sc-producer-section-title">{t("media")}</span>
    {#if support.mediaFile}
      <div class="fw-sc-producer-section-actions">
        <label class="fw-sc-producer-btn">
          {t("addMediaFile")}
          <input
            class="fw-sc-producer-file"
            type="file"
            accept="video/*,audio/*"
            onchange={handleFile}
          />
        </label>
      </div>
    {/if}
  </div>

  {#if !support.mediaFile}
    <p class="fw-sc-producer-notice">{t("mediaFileUnsupported")}</p>
  {/if}

  {#if fileFailed}
    <p class="fw-sc-producer-error" role="alert">{t("mediaFileError")}</p>
  {/if}

  {#if files.length === 0}
    <p class="fw-sc-producer-empty">{t("noMedia")}</p>
  {:else}
    <div class="fw-sc-media-list">
      {#each files as source (source.id)}
        <div class="fw-sc-media-item">
          <span class="fw-sc-overlay-name">{source.label}</span>
          <button
            type="button"
            class="fw-sc-producer-btn"
            onclick={() =>
              source.media?.playing ? pauseMediaSource(source.id) : playMediaSource(source.id)}
          >
            {source.media?.playing ? t("pauseMedia") : t("playMedia")}
          </button>
          <label class="fw-sc-media-loop">
            <input
              type="checkbox"
              checked={source.media?.loop}
              onchange={(e) =>
                setMediaSourceLoop(source.id, (e.target as HTMLInputElement).checked)}
            />
            {t("loopMedia")}
          </label>
        </div>
      {/each}
    </div>
  {/if}

  <p class="fw-sc-producer-notice">{t("mediaTabNotice")}</p>
</div>
