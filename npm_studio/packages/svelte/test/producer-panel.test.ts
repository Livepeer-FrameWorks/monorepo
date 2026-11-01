import { describe, it, expect, vi } from "vitest";
import { tick } from "svelte";
import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { readable } from "svelte/store";
import {
  createStudioTranslator,
  type Layer,
  type Scene,
  type TransitionConfig,
} from "@livepeer-frameworks/streamcrafter-core";
import StudioProducerPanel from "../src/components/StudioProducerPanel.svelte";
import type { CompositorState, CompositorStore } from "../src/stores/compositor";

function layer(id: string, zIndex: number): Layer {
  return {
    id,
    sourceId: `source-${id}`,
    visible: true,
    locked: false,
    zIndex,
  } as Layer;
}

function scene(id: string, name: string, layers: Layer[] = []): Scene {
  return { id, name, layers, backgroundColor: "#000000" };
}

function fakeCompositor() {
  return {
    createScene: vi.fn(),
    deleteScene: vi.fn(),
    setActiveScene: vi.fn(),
    transitionTo: vi.fn(async (_id: string, _transition?: TransitionConfig) => {}),
    setLayerVisibility: vi.fn(),
    reorderLayers: vi.fn(),
  };
}

function composition(overrides: Partial<CompositorState> = {}): CompositorState {
  return {
    isEnabled: true,
    isInitialized: true,
    rendererType: "canvas2d",
    stats: null,
    scenes: [
      scene("one", "Default", [layer("bottom", 0), layer("top", 1)]),
      scene("two", "Scene 2", [layer("only", 0)]),
    ],
    activeSceneId: "one",
    currentLayout: null,
    ...overrides,
  };
}

function renderPanel(
  state: CompositorState = composition(),
  compositor: ReturnType<typeof fakeCompositor> | null = fakeCompositor(),
  context: [string, unknown][] = []
) {
  const result = render(StudioProducerPanel, {
    context: new Map<string, unknown>([
      [
        "fw-sc-controller",
        {
          compositor: compositor as unknown as CompositorStore,
          compositorState: state,
          sources: [],
        },
      ],
      ...context,
    ]),
  });
  return { ...result, compositor: compositor! };
}

describe("StudioProducerPanel", () => {
  it("stays out of the way until the compositor runs", () => {
    const { container } = renderPanel(composition({ isInitialized: false }));
    expect(container.querySelector(".fw-sc-producer")).toBeNull();
  });

  it("adds a scene under the first unused number", async () => {
    const { compositor } = renderPanel(
      composition({ scenes: [scene("one", "Default"), scene("three", "Scene 3")] })
    );

    await fireEvent.click(screen.getByTitle("Create new scene"));

    expect(compositor.createScene).toHaveBeenCalledWith("Scene 4");
  });

  it("names new scenes in the studio locale", async () => {
    const { compositor } = renderPanel(composition(), fakeCompositor(), [
      ["fw-sc-translator", readable(createStudioTranslator({ locale: "nl" }))],
    ]);

    await fireEvent.click(screen.getByTitle("Nieuwe scène maken"));

    expect(compositor.createScene).toHaveBeenCalledWith("Scène 3");
  });

  it("switches scenes with the chosen transition and duration", async () => {
    const { compositor, container } = renderPanel();
    const select = container.querySelector("select") as HTMLSelectElement;
    const duration = container.querySelector("input[type=number]") as HTMLInputElement;

    await fireEvent.change(select, { target: { value: "slide-left" } });
    await fireEvent.input(duration, { target: { value: "800" } });
    await fireEvent.click(screen.getByText("Scene 2"));

    expect(compositor.transitionTo).toHaveBeenCalledWith("two", {
      type: "slide-left",
      durationMs: 800,
      easing: "ease-in-out",
    });
  });

  it("ignores scene clicks until the running transition has finished", async () => {
    let finish = () => {};
    const compositor = fakeCompositor();
    compositor.transitionTo.mockImplementation(
      () => new Promise<void>((resolve) => (finish = resolve))
    );
    renderPanel(
      composition({
        scenes: [scene("one", "Default"), scene("two", "Scene 2"), scene("three", "Scene 3")],
      }),
      compositor
    );

    await fireEvent.click(screen.getByText("Scene 2"));
    await fireEvent.click(screen.getByText("Scene 3"));
    expect(compositor.transitionTo).toHaveBeenCalledTimes(1);

    finish();
    await tick();
    await fireEvent.click(screen.getByText("Scene 3"));
    await waitFor(() => expect(compositor.transitionTo).toHaveBeenCalledTimes(2));
  });

  it("lists the active scene's layers and toggles their visibility", async () => {
    const { compositor } = renderPanel();

    await fireEvent.click(screen.getAllByTitle("Hide layer")[0]);

    expect(compositor.setLayerVisibility).toHaveBeenCalledWith("one", "top", false);
  });

  it("reorders layers bottom to top", async () => {
    const { compositor } = renderPanel();

    await fireEvent.click(screen.getAllByTitle("Move down")[0]);

    expect(compositor.reorderLayers).toHaveBeenCalledWith("one", ["top", "bottom"]);
  });

  it("counts layers in the studio locale", () => {
    renderPanel(composition(), fakeCompositor(), [
      ["fw-sc-translator", readable(createStudioTranslator({ locale: "de" }))],
    ]);

    expect(screen.getByText("2 Ebenen")).toBeTruthy();
    expect(screen.getByText("1 Ebene")).toBeTruthy();
  });

  it("keeps scene tiles on the theme surface", () => {
    const { container } = renderPanel();
    const tile = container.querySelector(".fw-sc-scene-item") as HTMLElement;

    expect(tile.style.backgroundColor).toBe("");
  });
});
