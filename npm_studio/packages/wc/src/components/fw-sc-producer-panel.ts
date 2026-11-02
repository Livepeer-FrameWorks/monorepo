/**
 * <fw-sc-producer-panel> — Producer controls for the compositor: scenes with
 * their transition, the layers of the active scene, overlays and media files.
 * Renders nothing until the compositor runs.
 * Port of StudioProducerPanel from streamcrafter-react.
 */
import { html, css, nothing } from "lit";
import { customElement } from "lit/decorators.js";
import {
  nextStudioSceneName,
  type TransitionConfig,
} from "@livepeer-frameworks/streamcrafter-core";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { StudioBoundElement } from "./studio-bound-element.js";
import "./fw-sc-scene-switcher.js";
import "./fw-sc-layer-list.js";
import "./fw-sc-overlay-controls.js";
import "./fw-sc-media-controls.js";

@customElement("fw-sc-producer-panel")
export class FwScProducerPanel extends StudioBoundElement {
  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  protected render() {
    const studio = this.boundStudio;
    const sceneManager = studio?.sceneManager;
    const activeScene = sceneManager?.getActiveScene();
    if (!studio || !sceneManager || !activeScene) return nothing;

    const scenes = sceneManager.getAllScenes();
    const changed = () => studio.requestUpdate();

    return html`
      <section class="fw-sc-producer" aria-label=${this.t("composition")}>
        <fw-sc-scene-switcher
          .scenes=${scenes}
          .activeSceneId=${activeScene.id}
          .t=${this.t}
          .onSceneSelect=${(sceneId: string) => {
            sceneManager.setActiveScene(sceneId);
            changed();
          }}
          .onSceneCreate=${() => {
            sceneManager.createScene(nextStudioSceneName(scenes, this.t));
            changed();
          }}
          .onSceneDelete=${(sceneId: string) => {
            sceneManager.deleteScene(sceneId);
            changed();
          }}
          .onTransitionTo=${async (sceneId: string, transition: TransitionConfig) => {
            const finished = sceneManager.transitionTo(sceneId, transition);
            changed();
            await finished;
          }}
        ></fw-sc-scene-switcher>
        <fw-sc-layer-list
          .layers=${[...activeScene.layers]}
          .sources=${studio.pc.s.sources}
          .t=${this.t}
          .onVisibilityToggle=${(layerId: string, visible: boolean) => {
            sceneManager.setLayerVisibility(activeScene.id, layerId, visible);
            changed();
          }}
          .onReorder=${(layerIds: string[]) => {
            sceneManager.reorderLayers(activeScene.id, layerIds);
            changed();
          }}
        ></fw-sc-layer-list>
        <fw-sc-overlay-controls .studio=${studio}></fw-sc-overlay-controls>
        <fw-sc-media-controls .studio=${studio}></fw-sc-media-controls>
      </section>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-producer-panel": FwScProducerPanel;
  }
}
