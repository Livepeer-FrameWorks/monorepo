<!--
  Producer controls for the compositor: scenes with their transition, the layers
  of the active scene, overlays and media files. Reads the compositor from the
  StreamCrafter context and renders nothing until the compositor is running.
-->
<script lang="ts">
  import { getContext } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import {
    createStudioTranslator,
    nextStudioSceneName,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";
  import type { CompositorContext } from "../stores/compositor";
  import SceneSwitcher from "./SceneSwitcher.svelte";
  import LayerList from "./LayerList.svelte";
  import StudioOverlayControls from "./StudioOverlayControls.svelte";
  import StudioMediaControls from "./StudioMediaControls.svelte";

  interface Props {
    class?: string;
  }

  let { class: className = "" }: Props = $props();

  const context = getContext<CompositorContext | undefined>("fw-sc-controller");
  const fallbackT = createStudioTranslator({ locale: "en" });
  const translatorCtx =
    getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator") ?? readable(fallbackT);
  let t = $derived($translatorCtx);

  let compositor = $derived(context?.compositor ?? null);
  let composition = $derived(context?.compositorState ?? null);
  let activeSceneId = $derived(composition?.activeSceneId ?? null);
  let activeScene = $derived(composition?.scenes.find((s) => s.id === activeSceneId) ?? null);

  function handleLayerVisibility(layerId: string, visible: boolean) {
    if (activeSceneId) compositor?.setLayerVisibility(activeSceneId, layerId, visible);
  }

  function handleLayerReorder(layerIds: string[]) {
    if (activeSceneId) compositor?.reorderLayers(activeSceneId, layerIds);
  }
</script>

{#if compositor && composition?.isEnabled && composition.isInitialized}
  <section class="fw-sc-producer {className}" aria-label={t("composition")}>
    <SceneSwitcher
      scenes={composition.scenes}
      {activeSceneId}
      onSceneSelect={compositor.setActiveScene}
      onSceneCreate={() =>
        compositor?.createScene(nextStudioSceneName(composition?.scenes ?? [], t))}
      onSceneDelete={compositor.deleteScene}
      onTransitionTo={compositor.transitionTo}
    />
    <LayerList
      layers={activeScene?.layers ?? []}
      sources={context?.sources ?? []}
      onVisibilityToggle={handleLayerVisibility}
      onReorder={handleLayerReorder}
    />
    <StudioOverlayControls {compositor} overlays={composition.overlays} />
    {#if context}
      <StudioMediaControls
        sources={context.sources}
        addMediaFile={context.addMediaFile}
        playMediaSource={context.playMediaSource}
        pauseMediaSource={context.pauseMediaSource}
        setMediaSourceLoop={context.setMediaSourceLoop}
      />
    {/if}
  </section>
{/if}
