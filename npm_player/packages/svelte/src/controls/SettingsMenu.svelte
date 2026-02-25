<script lang="ts">
  import { getContext } from "svelte";
  import { readable } from "svelte/store";
  import type { Readable } from "svelte/store";
  import { SettingsIcon } from "../icons";
  import {
    SPEED_PRESETS,
    getAvailableLocales,
    getLocaleDisplayName,
    createTranslator,
    type TranslateFn,
  } from "@livepeer-frameworks/player-core";
  import type { FwLocale } from "@livepeer-frameworks/player-core";

  interface Props {
    qualities?: Array<{ id: string; label: string; active?: boolean }>;
    activeQuality?: string;
    onSelectQuality?: (id: string) => void;
    textTracks?: Array<{ id: string; label: string; active?: boolean }>;
    activeCaption?: string;
    onSelectCaption?: (id: string) => void;
    playbackRate?: number;
    onSpeedChange?: (rate: number) => void;
    supportsSpeed?: boolean;
    playbackMode?: "auto" | "low-latency" | "quality";
    onModeChange?: (mode: "auto" | "low-latency" | "quality") => void;
    showModeSelector?: boolean;
    activeLocale?: FwLocale;
    onLocaleChange?: (locale: FwLocale) => void;
  }

  let {
    qualities: propQualities,
    activeQuality,
    onSelectQuality,
    textTracks: propTextTracks,
    activeCaption,
    onSelectCaption,
    playbackRate = 1,
    onSpeedChange,
    supportsSpeed = true,
    playbackMode,
    onModeChange,
    showModeSelector = false,
    activeLocale = undefined,
    onLocaleChange = undefined,
  }: Props = $props();

  let availableLocales = getAvailableLocales();

  let controller: any = getContext("fw-player-controller");
  const translatorStore: Readable<TranslateFn> =
    getContext<Readable<TranslateFn> | undefined>("fw-translator") ??
    readable(createTranslator({ locale: "en" }));
  let t: TranslateFn = $derived($translatorStore);
  let isOpen = $state(false);

  let qualities = $derived(propQualities ?? controller?.getQualities?.() ?? []);
  let qualityValue = $derived(activeQuality ?? qualities.find((q: any) => q.active)?.id ?? "auto");
  let textTracks = $derived(propTextTracks ?? []);
  let captionValue = $derived(activeCaption ?? textTracks.find((t: any) => t.active)?.id ?? "none");

  function selectQuality(id: string) {
    if (onSelectQuality) onSelectQuality(id);
    else controller?.selectQuality?.(id);
    isOpen = false;
  }

  function handleKeyDown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      isOpen = false;
      e.preventDefault();
      return;
    }
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const menu = e.currentTarget as HTMLElement;
      const items = menu.querySelectorAll<HTMLButtonElement>("button");
      if (!items.length) return;
      const current = Array.from(items).indexOf(document.activeElement as HTMLButtonElement);
      const next =
        e.key === "ArrowDown"
          ? (current + 1) % items.length
          : (current - 1 + items.length) % items.length;
      items[next]?.focus();
    }
  }
</script>

<div class="fw-control-group" style="position: relative">
  <button
    type="button"
    class="fw-btn-flush group"
    class:fw-btn-flush--active={isOpen}
    aria-label={t("settings")}
    title={t("settings")}
    onclick={() => (isOpen = !isOpen)}
  >
    <SettingsIcon size={16} />
  </button>

  {#if isOpen}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div class="fw-settings-menu" role="menu" aria-label={t("settings")} onkeydown={handleKeyDown}>
      {#if showModeSelector && onModeChange}
        <div class="fw-settings-section">
          <div class="fw-settings-label">{t("mode")}</div>
          <div class="fw-settings-options">
            {#each ["auto", "low-latency", "quality"] as mode}
              <button
                class="fw-settings-btn"
                class:fw-settings-btn--active={playbackMode === mode}
                onclick={() => {
                  onModeChange?.(mode as any);
                  isOpen = false;
                }}
              >
                {mode === "low-latency" ? t("fast") : mode === "quality" ? t("stable") : t("auto")}
              </button>
            {/each}
          </div>
        </div>
      {/if}

      {#if supportsSpeed}
        <div class="fw-settings-section">
          <div class="fw-settings-label">{t("speed")}</div>
          <div class="fw-settings-options fw-settings-options--wrap">
            {#each SPEED_PRESETS as rate}
              <button
                class="fw-settings-btn"
                class:fw-settings-btn--active={playbackRate === rate}
                onclick={() => {
                  onSpeedChange?.(rate);
                  isOpen = false;
                }}
              >
                {rate}x
              </button>
            {/each}
          </div>
        </div>
      {/if}

      {#if qualities.length > 0}
        <div class="fw-settings-section">
          <div class="fw-settings-label">{t("quality")}</div>
          <div class="fw-settings-list">
            <button
              class="fw-settings-list-item"
              class:fw-settings-list-item--active={qualityValue === "auto"}
              onclick={() => selectQuality("auto")}>{t("auto")}</button
            >
            {#each qualities as q}
              <button
                class="fw-settings-list-item"
                class:fw-settings-list-item--active={qualityValue === q.id}
                onclick={() => selectQuality(q.id)}>{q.label}</button
              >
            {/each}
          </div>
        </div>
      {/if}

      {#if textTracks.length > 0}
        <div class="fw-settings-section">
          <div class="fw-settings-label">{t("captions")}</div>
          <div class="fw-settings-list">
            <button
              class="fw-settings-list-item"
              class:fw-settings-list-item--active={captionValue === "none"}
              onclick={() => {
                onSelectCaption?.("none");
                isOpen = false;
              }}>{t("captionsOff")}</button
            >
            {#each textTracks as tt}
              <button
                class="fw-settings-list-item"
                class:fw-settings-list-item--active={captionValue === tt.id}
                onclick={() => {
                  onSelectCaption?.(tt.id);
                  isOpen = false;
                }}>{tt.label || tt.id}</button
              >
            {/each}
          </div>
        </div>
      {/if}

      {#if onLocaleChange}
        <div class="fw-settings-section">
          <div class="fw-settings-label">{t("language")}</div>
          <div class="fw-settings-list">
            {#each availableLocales as l}
              <button
                class="fw-settings-list-item"
                class:fw-settings-list-item--active={activeLocale === l}
                onclick={() => {
                  onLocaleChange?.(l);
                  isOpen = false;
                }}>{getLocaleDisplayName(l)}</button
              >
            {/each}
          </div>
        </div>
      {/if}
    </div>
  {/if}
</div>
