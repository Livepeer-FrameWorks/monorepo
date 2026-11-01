<!--
  The Add Camera action. With more than one camera to choose from it lists the
  video inputs; the first camera on a single-camera machine starts straight away.
-->
<script lang="ts">
  import { getContext, onDestroy } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    type CameraChoice,
    type MediaSource,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import CameraIcon from "../icons/CameraIcon.svelte";

  interface Props {
    sources: MediaSource[];
    disabled?: boolean;
    getChoices: () => Promise<CameraChoice[]>;
    onPick: (deviceId?: string) => void;
  }

  let { sources, disabled = false, getChoices, onPick }: Props = $props();

  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let choices = $state<CameraChoice[] | null>(null);
  let rootEl = $state<HTMLDivElement>();

  function close() {
    choices = null;
    document.removeEventListener("mousedown", handleOutsideClick);
    document.removeEventListener("keydown", handleEscape);
  }

  function handleOutsideClick(e: MouseEvent) {
    if (rootEl && !rootEl.contains(e.target as Node)) close();
  }

  function handleEscape(e: KeyboardEvent) {
    if (e.key === "Escape") close();
  }

  async function handleClick() {
    if (choices) {
      close();
      return;
    }
    const available = await getChoices();
    const hasCamera = sources.some((source) => source.type === "camera");
    if (!hasCamera && available.length <= 1) {
      onPick(available[0]?.deviceId);
      return;
    }
    choices = available;
    document.addEventListener("mousedown", handleOutsideClick);
    document.addEventListener("keydown", handleEscape);
  }

  function pick(deviceId: string) {
    close();
    onPick(deviceId);
  }

  onDestroy(close);
</script>

<div class="fw-sc-camera-picker" bind:this={rootEl}>
  <button
    type="button"
    class="fw-sc-action-secondary"
    onclick={handleClick}
    {disabled}
    title={t("addCamera")}
    aria-haspopup="menu"
    aria-expanded={choices !== null}
  >
    <CameraIcon size={18} />
  </button>
  {#if choices}
    <div class="fw-sc-camera-menu" role="menu" aria-label={t("chooseCamera")}>
      <div class="fw-sc-camera-menu-title">{t("chooseCamera")}</div>
      {#each choices as choice (choice.deviceId)}
        <button
          type="button"
          role="menuitem"
          class="fw-sc-context-menu-item"
          disabled={choice.inUse}
          onclick={() => pick(choice.deviceId)}
        >
          <span>{choice.label}</span>
          {#if choice.inUse}
            <span class="fw-sc-camera-menu-note">{t("cameraActive")}</span>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
</div>
