<script lang="ts">
  import { getContext } from "svelte";
  import type { Snippet } from "svelte";
  import type { Readable } from "svelte/store";
  import { readable } from "svelte/store";
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
    /** Open the popup on mount */
    defaultOpen?: boolean;
    /** Extra sections below the quality profiles */
    children?: Snippet;
  }

  let {
    qualityProfile: propProfile,
    onProfileChange,
    autoClose = true,
    defaultOpen = false,
    children,
  }: Props = $props();

  let pc: any = getContext("fw-sc-controller");
  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);
  // svelte-ignore state_referenced_locally
  let isOpen = $state(defaultOpen);
  let dropdownEl = $state<HTMLDivElement>(undefined!);
  let buttonEl = $state<HTMLButtonElement>(undefined!);

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
    aria-expanded={isOpen}
  >
    <SettingsIcon size={16} />
  </button>
  {#if isOpen}
    <div bind:this={dropdownEl} class="fw-sc-settings-popup">
      <div class="fw-sc-dropdown-section">
        <div class="fw-sc-dropdown-label">{t("quality")}</div>
        <div class="fw-sc-dropdown-options">
          {#each QUALITY_PROFILES as p (p.id)}
            <button
              type="button"
              class="fw-sc-dropdown-option {profile === p.id
                ? 'fw-sc-dropdown-option--active'
                : ''}"
              onclick={() => handleSelect(p.id)}
              disabled={pc?.isStreaming}
            >
              <span class="fw-sc-dropdown-option-label">{p.label}</span>
              <span class="fw-sc-dropdown-option-desc">{p.description}</span>
            </button>
          {/each}
        </div>
      </div>
      {@render children?.()}
    </div>
  {/if}
</div>
