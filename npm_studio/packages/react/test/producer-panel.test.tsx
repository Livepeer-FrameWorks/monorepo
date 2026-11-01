import React from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import type { Scene } from "@livepeer-frameworks/streamcrafter-core";
import { StudioProducerPanel } from "../src/components/StudioProducerPanel";
import { StudioI18nProvider } from "../src/context/StudioI18nContext";
import { useCompositor, type UseCompositorReturn } from "../src/hooks/useCompositor";

afterEach(cleanup);

function scene(id: string, name: string, layerIds: string[] = []): Scene {
  return {
    id,
    name,
    backgroundColor: "#000000",
    layers: layerIds.map((layerId, zIndex) => ({
      id: layerId,
      sourceId: `source-${layerId}`,
      visible: true,
      zIndex,
    })),
  } as Scene;
}

function fakeCompositor(overrides: Partial<UseCompositorReturn> = {}) {
  const scenes = [scene("one", "Default", ["bottom", "top"]), scene("two", "Scene 2", ["only"])];
  return {
    isEnabled: true,
    isInitialized: true,
    scenes,
    activeScene: scenes[0],
    activeSceneId: "one",
    createScene: vi.fn(),
    deleteScene: vi.fn(),
    setActiveScene: vi.fn(),
    transitionTo: vi.fn(async () => {}),
    setLayerVisibility: vi.fn(),
    reorderLayers: vi.fn(),
    ...overrides,
  } as unknown as UseCompositorReturn;
}

describe("StudioProducerPanel", () => {
  it("stays out of the way until the compositor runs", () => {
    const { container } = render(
      <StudioProducerPanel compositor={fakeCompositor({ isInitialized: false })} sources={[]} />
    );
    expect(container.querySelector(".fw-sc-producer")).toBeNull();
  });

  it("adds a scene under the first unused number", () => {
    const compositor = fakeCompositor();
    render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    fireEvent.click(screen.getByTitle("Create new scene"));

    expect(compositor.createScene).toHaveBeenCalledWith("Scene 3");
  });

  it("switches scenes with the chosen transition", async () => {
    const compositor = fakeCompositor();
    const { container } = render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    fireEvent.change(container.querySelector("select")!, { target: { value: "slide-up" } });
    await act(async () => {
      fireEvent.click(screen.getByText("Scene 2"));
    });

    expect(compositor.transitionTo).toHaveBeenCalledWith(
      "two",
      expect.objectContaining({ type: "slide-up" })
    );
  });

  it("toggles layer visibility in the active scene", () => {
    const compositor = fakeCompositor();
    render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    fireEvent.click(screen.getAllByTitle("Hide layer")[0]);

    expect(compositor.setLayerVisibility).toHaveBeenCalledWith("one", "top", false);
  });

  it("reorders layers bottom to top", () => {
    const compositor = fakeCompositor();
    render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    fireEvent.click(screen.getAllByTitle("Move down")[0]);

    expect(compositor.reorderLayers).toHaveBeenCalledWith("one", ["top", "bottom"]);
  });

  it("counts layers in the studio locale", () => {
    render(
      <StudioI18nProvider locale="de">
        <StudioProducerPanel compositor={fakeCompositor()} sources={[]} />
      </StudioI18nProvider>
    );

    expect(screen.getByText("2 Ebenen")).toBeTruthy();
    expect(screen.getByText("1 Ebene")).toBeTruthy();
  });

  it("keeps scene tiles on the theme surface", () => {
    const { container } = render(
      <StudioProducerPanel compositor={fakeCompositor()} sources={[]} />
    );
    const tile = container.querySelector(".fw-sc-scene-item") as HTMLElement;

    expect(tile.style.backgroundColor).toBe("");
  });
});

describe("useCompositor", () => {
  it("follows scene changes that happen after the compositor came up", async () => {
    const listeners = new Map<string, () => void>();
    const scenes = [scene("one", "Default")];
    const sceneManager = {
      on: (event: string, handler: () => void) => {
        listeners.set(event, handler);
        return () => listeners.delete(event);
      },
      getAllScenes: () => [...scenes],
      getActiveScene: () => scenes[0],
      getRendererType: () => "canvas2d",
      getStats: () => ({ fps: 30, frameTimeMs: 1 }),
      getCurrentLayout: () => ({ mode: "solo" }),
      isInitialized: () => true,
    };
    let enabled = false;
    const controller = {
      getSceneManager: () => (enabled ? sceneManager : null),
      enableCompositor: vi.fn(async () => {
        enabled = true;
      }),
    };

    const { result } = renderHook(() =>
      useCompositor({ controller: controller as never, autoEnable: true })
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(result.current.scenes).toHaveLength(1);

    scenes.push(scene("two", "Scene 2"));
    act(() => listeners.get("sceneCreated")?.());

    expect(result.current.scenes).toHaveLength(2);
  });
});
