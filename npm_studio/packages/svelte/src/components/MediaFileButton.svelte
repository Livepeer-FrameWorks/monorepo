<!--
  The Add Media File action: picks a local video or audio file and plays it into
  the mix. A browser that cannot capture a media element gets the button
  disabled with the reason as its title.
-->
<script lang="ts">
  import { getContext, onMount } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    detectStudioSupport,
    FULL_STUDIO_SUPPORT,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import FilmIcon from "../icons/FilmIcon.svelte";

  interface Props {
    onAdd: (file: File) => Promise<unknown>;
    disabled?: boolean;
  }

  let { onAdd, disabled = false }: Props = $props();

  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let support = $state(FULL_STUDIO_SUPPORT);
  onMount(() => {
    support = detectStudioSupport();
  });

  let inputEl = $state<HTMLInputElement>(undefined!);
  let failed = $state(false);

  async function handleChange(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    failed = false;
    try {
      await onAdd(file);
    } catch (err) {
      console.error("Failed to add media file:", err);
      failed = true;
    }
  }
</script>

<div class="fw-sc-camera-picker">
  <button
    type="button"
    class="fw-sc-action-secondary"
    onclick={() => {
      failed = false;
      inputEl?.click();
    }}
    disabled={disabled || !support.mediaFile}
    title={support.mediaFile ? t("addMediaFile") : t("mediaFileUnsupported")}
  >
    <FilmIcon size={18} />
  </button>
  <input
    bind:this={inputEl}
    class="fw-sc-hidden"
    type="file"
    accept="video/*,audio/*"
    tabindex="-1"
    aria-hidden="true"
    onchange={handleChange}
  />
  {#if failed}
    <div class="fw-sc-camera-menu" role="alert">
      <div class="fw-sc-camera-menu-note">{t("mediaFileError")}</div>
    </div>
  {/if}
</div>
