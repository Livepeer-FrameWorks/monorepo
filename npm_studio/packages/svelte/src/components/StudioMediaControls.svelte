<!--
  Local media files in the mix, with their playback controls. Files are added
  from the action bar; volume, mute and removal live with every other source in
  the mixer.
-->
<script lang="ts">
  import { getContext } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    type MediaSource,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";

  interface Props {
    sources: MediaSource[];
    playMediaSource: (sourceId: string) => void;
    pauseMediaSource: (sourceId: string) => void;
    setMediaSourceLoop: (sourceId: string, loop: boolean) => void;
  }

  let { sources, playMediaSource, pauseMediaSource, setMediaSourceLoop }: Props = $props();

  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let files = $derived(sources.filter((source) => source.media));
</script>

<div class="fw-sc-producer-section">
  <div class="fw-sc-producer-section-header">
    <span class="fw-sc-producer-section-title">{t("media")}</span>
  </div>

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
