<!--
  Logo and text overlays of the active scene, with their position, size and text.
-->
<script lang="ts">
  import { getContext } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    TEXT_OVERLAY_DEFAULTS,
    type StudioOverlay,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import type { CompositorStore } from "../stores/compositor";
  import XIcon from "../icons/XIcon.svelte";

  interface Props {
    compositor: CompositorStore;
    overlays: StudioOverlay[];
  }

  let { compositor, overlays }: Props = $props();

  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let imageFailed = $state(false);

  async function handleImage(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;

    imageFailed = false;
    try {
      await compositor.addImageOverlay(file);
    } catch (err) {
      console.error("Failed to add image overlay:", err);
      imageFailed = true;
    }
  }

  function percent(e: Event): number {
    return Number((e.target as HTMLInputElement).value) / 100;
  }
</script>

<div class="fw-sc-producer-section">
  <div class="fw-sc-producer-section-header">
    <span class="fw-sc-producer-section-title">{t("overlays")}</span>
    <div class="fw-sc-producer-section-actions">
      <label class="fw-sc-producer-btn">
        {t("addLogo")}
        <input class="fw-sc-producer-file" type="file" accept="image/*" onchange={handleImage} />
      </label>
      <button
        type="button"
        class="fw-sc-producer-btn"
        onclick={() => compositor.addTextOverlay({ text: t("overlayDefaultText") })}
      >
        {t("addText")}
      </button>
    </div>
  </div>

  {#if imageFailed}
    <p class="fw-sc-producer-error" role="alert">{t("overlayImageError")}</p>
  {/if}

  {#if overlays.length === 0}
    <p class="fw-sc-producer-empty">{t("noOverlays")}</p>
  {:else}
    <div class="fw-sc-overlay-list">
      {#each overlays as overlay (overlay.sourceId)}
        <div class="fw-sc-overlay-item">
          <div class="fw-sc-overlay-header">
            <span class="fw-sc-overlay-name">{overlay.label}</span>
            <button
              type="button"
              class="fw-sc-icon-btn fw-sc-icon-btn--destructive"
              title={t("removeOverlay")}
              onclick={() => compositor.removeOverlay(overlay.sourceId)}
            >
              <XIcon size={14} />
            </button>
          </div>

          {#if overlay.text}
            <label class="fw-sc-overlay-field">
              {t("overlayText")}
              <textarea
                rows="2"
                value={overlay.text.text}
                oninput={(e) =>
                  compositor.updateTextOverlay(overlay.sourceId, {
                    text: (e.target as HTMLTextAreaElement).value,
                  })}
              ></textarea>
            </label>
            <label class="fw-sc-overlay-field">
              {t("overlayFontSize")}
              <input
                type="range"
                min={TEXT_OVERLAY_DEFAULTS.minFontSize}
                max={TEXT_OVERLAY_DEFAULTS.maxFontSize}
                value={overlay.text.fontSize ?? TEXT_OVERLAY_DEFAULTS.fontSize}
                oninput={(e) =>
                  compositor.updateTextOverlay(overlay.sourceId, {
                    fontSize: Number((e.target as HTMLInputElement).value),
                  })}
              />
            </label>
            <label class="fw-sc-overlay-field">
              {t("overlayColor")}
              <input
                type="color"
                value={overlay.text.color ?? TEXT_OVERLAY_DEFAULTS.color}
                oninput={(e) =>
                  compositor.updateTextOverlay(overlay.sourceId, {
                    color: (e.target as HTMLInputElement).value,
                  })}
              />
            </label>
          {:else}
            <label class="fw-sc-overlay-field">
              {t("overlaySize")}
              <input
                type="range"
                min="5"
                max="100"
                value={Math.round(overlay.size * 100)}
                oninput={(e) =>
                  compositor.setOverlayPlacement(overlay.sourceId, { size: percent(e) })}
              />
            </label>
          {/if}

          <label class="fw-sc-overlay-field">
            {t("overlayPositionX")}
            <input
              type="range"
              min="0"
              max="100"
              value={Math.round(overlay.x * 100)}
              oninput={(e) => compositor.setOverlayPlacement(overlay.sourceId, { x: percent(e) })}
            />
          </label>
          <label class="fw-sc-overlay-field">
            {t("overlayPositionY")}
            <input
              type="range"
              min="0"
              max="100"
              value={Math.round(overlay.y * 100)}
              oninput={(e) => compositor.setOverlayPlacement(overlay.sourceId, { y: percent(e) })}
            />
          </label>
        </div>
      {/each}
    </div>
  {/if}

  {#if overlays.some((overlay) => overlay.kind === "image")}
    <p class="fw-sc-producer-notice">{t("overlayLocalNotice")}</p>
  {/if}
</div>
