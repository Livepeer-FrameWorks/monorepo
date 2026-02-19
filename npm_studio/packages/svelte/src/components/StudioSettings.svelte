<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import {
    createStudioTranslator,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import SettingsIcon from "../icons/SettingsIcon.svelte";
  import type { QualityProfile } from "@livepeer-frameworks/streamcrafter-core";

  interface Props {
    qualityProfile?: QualityProfile;
    onProfileChange?: (profile: QualityProfile) => void;
    autoClose?: boolean;
  }

  let { qualityProfile: propProfile, onProfileChange, autoClose = true }: Props = $props();

  let pc: any = getContext("fw-sc-controller");
  const translatorCtx = getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator");
  const fallbackT = createStudioTranslator({ locale: "en" });
  let t: StudioTranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);
  let isOpen = $state(false);
  let dropdownEl: HTMLDivElement;
  let buttonEl: HTMLButtonElement;

  let profile = $derived(propProfile ?? pc?.qualityProfile ?? "broadcast");

  let QUALITY_PROFILES = $derived([
    {
      id: "professional" as QualityProfile,
      label: t("professional"),
      description: t("professionalDesc"),
    },
    { id: "broadcast" as QualityProfile, label: t("broadcast"), description: t("broadcastDesc") },
    {
      id: "conference" as QualityProfile,
      label: t("conference"),
      description: t("conferenceDesc"),
    },
  ]);

  $effect(() => {
    if (!isOpen) return;
    function handleClickOutside(e: MouseEvent) {
      const target = e.target as Node;
      if (dropdownEl && !dropdownEl.contains(target) && buttonEl && !buttonEl.contains(target)) {
        isOpen = false;
      }
    }
    function handleEscape(e: KeyboardEvent) {
      if (e.key === "Escape") isOpen = false;
    }
    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleEscape);
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
    };
  });

  function handleSelect(id: QualityProfile) {
    if (pc?.isStreaming) return;
    if (onProfileChange) onProfileChange(id);
    else pc?.setQualityProfile(id);
    if (autoClose) isOpen = false;
  }
</script>

<div style="position: relative">
  <button
    bind:this={buttonEl}
    type="button"
    class="fw-sc-action-secondary {isOpen ? 'fw-sc-action-secondary--active' : ''}"
    onclick={() => (isOpen = !isOpen)}
    title={t("settings")}
    style="display: flex; align-items: center; justify-content: center;"
  >
    <SettingsIcon size={16} />
  </button>
  {#if isOpen}
    <div
      bind:this={dropdownEl}
      style="
        position: absolute;
        bottom: 100%;
        left: 0;
        margin-bottom: 8px;
        width: 192px;
        background: #1a1b26;
        border: 1px solid rgba(90, 96, 127, 0.3);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
        border-radius: 4px;
        overflow: hidden;
        z-index: 50;
      "
    >
      <div style="padding: 8px;">
        <div
          style="font-size: 10px; color: #565f89; text-transform: uppercase; font-weight: 600; margin-bottom: 4px; padding-left: 4px;"
        >
          {t("quality")}
        </div>
        <div style="display: flex; flex-direction: column; gap: 2px;">
          {#each QUALITY_PROFILES as p}
            <button
              type="button"
              onclick={() => handleSelect(p.id)}
              disabled={pc?.isStreaming}
              style="
                width: 100%;
                padding: 6px 8px;
                text-align: left;
                font-size: 12px;
                border-radius: 4px;
                transition: all 0.15s;
                border: none;
                cursor: {pc?.isStreaming ? 'not-allowed' : 'pointer'};
                opacity: {pc?.isStreaming ? 0.5 : 1};
                background: {profile === p.id ? 'rgba(122, 162, 247, 0.2)' : 'transparent'};
                color: {profile === p.id ? '#7aa2f7' : '#a9b1d6'};
              "
            >
              <div style="font-weight: 500;">{p.label}</div>
              <div style="font-size: 10px; color: #565f89;">{p.description}</div>
            </button>
          {/each}
        </div>
      </div>
    </div>
  {/if}
</div>
