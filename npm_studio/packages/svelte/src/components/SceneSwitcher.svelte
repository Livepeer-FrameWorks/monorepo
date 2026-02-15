<script lang="ts">
  import { onMount } from "svelte";
  import type {
    Scene,
    TransitionConfig,
    TransitionType,
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
    <span class="fw-sc-scene-switcher-title">Scenes</span>
    {#if showTransitionControls}
      <div class="fw-sc-transition-controls">
        <select
          class="fw-sc-transition-select"
          value={selectedTransition}
          onchange={(e) =>
            (selectedTransition = (e.target as HTMLSelectElement).value as TransitionType)}
        >
          <option value="cut">Cut</option>
          <option value="fade">Fade</option>
          <option value="slide-left">Slide Left</option>
          <option value="slide-right">Slide Right</option>
          <option value="slide-up">Slide Up</option>
          <option value="slide-down">Slide Down</option>
        </select>
        <input
          type="number"
          class="fw-sc-transition-duration"
          value={transitionDuration}
          oninput={(e) => (transitionDuration = Number((e.target as HTMLInputElement).value))}
          min={0}
          max={3000}
          step={100}
          title="Transition duration (ms)"
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
            title="Delete scene"
          >
            ×
          </button>
        {/if}
      </div>
    {/each}

    {#if onSceneCreate}
      <button class="fw-sc-scene-add" onclick={onSceneCreate} title="Create new scene">+</button>
    {/if}
  </div>
</div>
