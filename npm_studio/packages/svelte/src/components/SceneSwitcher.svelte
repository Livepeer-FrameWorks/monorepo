<script lang="ts">
  import { onMount, getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import type {
    Scene,
    TransitionConfig,
    TransitionType,
  } from "@livepeer-frameworks/streamcrafter-core";
  import {
    createStudioTranslator,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";

  interface Props {
    scenes: Scene[];
    activeSceneId: string | null;
    onSceneSelect: (sceneId: string) => void;
    onSceneCreate?: () => void;
    onSceneDelete?: (sceneId: string) => void;
    onTransitionTo?: (sceneId: string, transition: TransitionConfig) => Promise<void>;
    transitionConfig?: TransitionConfig;
    showTransitionControls?: boolean;
    class?: string;
  }

  const DEFAULT_TRANSITION: TransitionConfig = {
    type: "fade",
    durationMs: 500,
    easing: "ease-in-out",
  };

  let {
    scenes = [],
    activeSceneId,
    onSceneSelect,
    onSceneCreate,
    onSceneDelete,
    onTransitionTo,
    transitionConfig = DEFAULT_TRANSITION,
    showTransitionControls = true,
    class: className = "",
  }: Props = $props();

  const translatorCtx = getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator");
  const fallbackT = createStudioTranslator({ locale: "en" });
  let t: StudioTranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);

  let selectedTransition = $state<TransitionType>("fade");
  let transitionDuration = $state(500);
  let isTransitioning = $state(false);

  onMount(() => {
    selectedTransition = transitionConfig.type;
    transitionDuration = transitionConfig.durationMs;
  });

  async function handleSceneClick(sceneId: string) {
    if (sceneId === activeSceneId || isTransitioning) return;

    if (onTransitionTo) {
      isTransitioning = true;
      try {
        await onTransitionTo(sceneId, {
          type: selectedTransition,
          durationMs: transitionDuration,
          easing: transitionConfig.easing,
        });
      } finally {
        isTransitioning = false;
      }
      return;
    }

    onSceneSelect(sceneId);
  }

  function handleDeleteClick(e: MouseEvent, sceneId: string) {
    e.stopPropagation();
    if (scenes.length <= 1) return;
    onSceneDelete?.(sceneId);
  }
</script>

<div class="fw-sc-scene-switcher {className}">
  <div class="fw-sc-scene-switcher-header">
    <span class="fw-sc-scene-switcher-title">{t("scenes")}</span>
    {#if showTransitionControls}
      <div class="fw-sc-transition-controls">
        <select
          class="fw-sc-transition-select"
          value={selectedTransition}
          onchange={(e) =>
            (selectedTransition = (e.target as HTMLSelectElement).value as TransitionType)}
        >
          <option value="cut">{t("cut")}</option>
          <option value="fade">{t("fade")}</option>
          <option value="slide-left">{t("slideLeft")}</option>
          <option value="slide-right">{t("slideRight")}</option>
          <option value="slide-up">{t("slideUp")}</option>
          <option value="slide-down">{t("slideDown")}</option>
        </select>
        <input
          type="number"
          class="fw-sc-transition-duration"
          value={transitionDuration}
          oninput={(e) => (transitionDuration = Number((e.target as HTMLInputElement).value))}
          min={0}
          max={3000}
          step={100}
          title={t("transitionDuration")}
        />
        <span class="fw-sc-transition-unit">ms</span>
      </div>
    {/if}
  </div>

  <div class="fw-sc-scene-list">
    {#each scenes as scene (scene.id)}
      <div
        class="fw-sc-scene-item {scene.id === activeSceneId
          ? 'fw-sc-scene-item--active'
          : ''} {isTransitioning ? 'fw-sc-scene-item--transitioning' : ''}"
        role="button"
        tabindex="0"
        onclick={() => handleSceneClick(scene.id)}
        onkeydown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            handleSceneClick(scene.id);
          }
        }}
        style="background-color: {scene.backgroundColor}"
      >
        <span class="fw-sc-scene-name">{scene.name}</span>
        <span class="fw-sc-scene-layer-count">{scene.layers.length} layers</span>
        {#if onSceneDelete && scenes.length > 1 && scene.id !== activeSceneId}
          <button
            class="fw-sc-scene-delete"
            onclick={(e) => handleDeleteClick(e, scene.id)}
            title={t("deleteScene")}
          >
            ×
          </button>
        {/if}
      </div>
    {/each}

    {#if onSceneCreate}
      <button class="fw-sc-scene-add" onclick={onSceneCreate} title={t("createNewScene")}>+</button>
    {/if}
  </div>
</div>
