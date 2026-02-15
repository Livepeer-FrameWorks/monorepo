import { afterEach, describe, expect, it, vi } from "vitest";
import { FwScSceneSwitcher } from "../src/components/fw-sc-scene-switcher.js";

function createScenes() {
  return [
    {
      id: "scene-1",
      name: "Scene 1",
      backgroundColor: "#111111",
      layers: [{ id: "layer-1" }],
    },
    {
      id: "scene-2",
      name: "Scene 2",
      backgroundColor: "#222222",
      layers: [{ id: "layer-2" }, { id: "layer-3" }],
    },
  ] as any[];
}

function createElement() {
  const el = new FwScSceneSwitcher();
  el.scenes = createScenes() as any;
  el.activeSceneId = "scene-1";
  document.body.appendChild(el);
  return el;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("FwScSceneSwitcher parity", () => {
  it("hides create/delete controls when optional handlers are not provided", async () => {
    const el = createElement();
    await el.updateComplete;

    expect(el.shadowRoot?.querySelector(".fw-sc-scene-add")).toBeNull();
    expect(el.shadowRoot?.querySelector(".fw-sc-scene-delete")).toBeNull();
  });

  it("shows and wires create/delete controls when optional handlers are provided", async () => {
    const onSceneCreate = vi.fn();
    const onSceneDelete = vi.fn();
    const el = createElement();
    el.onSceneCreate = onSceneCreate;
    el.onSceneDelete = onSceneDelete;
    await el.updateComplete;

    const addButton = el.shadowRoot?.querySelector(".fw-sc-scene-add") as HTMLButtonElement | null;
    expect(addButton).not.toBeNull();
    addButton?.click();
    expect(onSceneCreate).toHaveBeenCalledTimes(1);

    const deleteButton = el.shadowRoot?.querySelector(
      ".fw-sc-scene-delete"
    ) as HTMLButtonElement | null;
    expect(deleteButton).not.toBeNull();
    deleteButton?.click();
    expect(onSceneDelete).toHaveBeenCalledWith("scene-2");
  });

  it("calls onSceneSelect when no transition callback is provided", async () => {
    const onSceneSelect = vi.fn();
    const el = createElement();
    el.onSceneSelect = onSceneSelect;
    await el.updateComplete;

    const items = el.shadowRoot?.querySelectorAll(".fw-sc-scene-item");
    const nextSceneItem = items?.[1] as HTMLElement | undefined;
    nextSceneItem?.click();

    expect(onSceneSelect).toHaveBeenCalledWith("scene-2");
  });

  it("awaits onTransitionTo and keeps transitioning state until completion", async () => {
    let resolveTransition: (() => void) | null = null;
    const transitionDone = new Promise<void>((resolve) => {
      resolveTransition = resolve;
    });
    const onTransitionTo = vi.fn(() => transitionDone);

    const el = new FwScSceneSwitcher();
    el.scenes = createScenes() as any;
    el.activeSceneId = "scene-1";
    el.transitionConfig = {
      type: "slide-left",
      durationMs: 700,
      easing: "ease-in-out",
    } as any;
    el.onTransitionTo = onTransitionTo;
    document.body.appendChild(el);
    await el.updateComplete;

    const items = el.shadowRoot?.querySelectorAll(".fw-sc-scene-item");
    const nextSceneItem = items?.[1] as HTMLElement | undefined;
    nextSceneItem?.click();
    await el.updateComplete;

    expect(onTransitionTo).toHaveBeenCalledWith("scene-2", {
      type: "slide-left",
      durationMs: 700,
      easing: "ease-in-out",
    });
    expect(el.shadowRoot?.querySelector(".fw-sc-scene-item--transitioning")).not.toBeNull();

    resolveTransition?.();
    await transitionDone;
    await el.updateComplete;

    expect(el.shadowRoot?.querySelector(".fw-sc-scene-item--transitioning")).toBeNull();
  });
});
