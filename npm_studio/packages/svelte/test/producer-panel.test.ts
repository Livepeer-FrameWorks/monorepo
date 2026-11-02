import { afterAll, beforeAll, describe, it, expect, vi } from "vitest";
import { tick } from "svelte";
import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { readable } from "svelte/store";
import {
  createStudioTranslator,
  type Layer,
  type MediaSource,
  type Scene,
  type StudioOverlay,
  type TransitionConfig,
} from "@livepeer-frameworks/streamcrafter-core";
import StudioProducerPanel from "../src/components/StudioProducerPanel.svelte";
import MediaFileButton from "../src/components/MediaFileButton.svelte";
import type { CompositorState, CompositorStore } from "../src/stores/compositor";

// jsdom media elements cannot capture; the panel offers media files to those that can.
beforeAll(() => {
  Object.assign(HTMLMediaElement.prototype, { captureStream: () => null });
});
afterAll(() => {
  delete (HTMLMediaElement.prototype as Partial<HTMLMediaElement>).captureStream;
});

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
    removeLayer: vi.fn(),
    updateLayerTransform: vi.fn(),
    addImageOverlay: vi.fn(async (_image: Blob) => {}),
    addTextOverlay: vi.fn(),
    updateTextOverlay: vi.fn(),
    setOverlayPlacement: vi.fn(),
    removeOverlay: vi.fn(),
  };
}

function fakeMedia() {
  return {
    playMediaSource: vi.fn(),
    pauseMediaSource: vi.fn(),
    setMediaSourceLoop: vi.fn(),
  };
}

const textOverlay: StudioOverlay = {
  sourceId: "text-1",
  layerId: "layer-text",
  kind: "text",
  label: "Ada",
  x: 0.04,
  y: 0.92,
  size: 0.2,
  text: { text: "Ada", fontSize: 48, color: "#ffffff", style: "lower-third" },
};

const imageOverlay: StudioOverlay = {
  sourceId: "img-1",
  layerId: "layer-img",
  kind: "image",
  label: "logo.png",
  x: 0.97,
  y: 0.05,
  size: 0.15,
};

const mediaSource = (overrides: Partial<MediaSource["media"]> = {}) =>
  ({
    id: "media-1",
    type: "media",
    label: "clip.mp4",
    media: {
      playing: true,
      loop: false,
      duration: 8,
      hasVideo: true,
      hasAudio: true,
      ...overrides,
    },
  }) as MediaSource;

function pick(input: HTMLInputElement, file: File) {
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  return fireEvent.change(input);
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
    overlays: [],
    error: null,
    ...overrides,
  };
}

function renderPanel(
  state: CompositorState = composition(),
  compositor: ReturnType<typeof fakeCompositor> | null = fakeCompositor(),
  context: [string, unknown][] = [],
  sources: MediaSource[] = [],
  media: ReturnType<typeof fakeMedia> = fakeMedia()
) {
  const result = render(StudioProducerPanel, {
    context: new Map<string, unknown>([
      [
        "fw-sc-controller",
        {
          compositor: compositor as unknown as CompositorStore,
          compositorState: state,
          sources,
          ...media,
        },
      ],
      ...context,
    ]),
  });
  return { ...result, compositor: compositor!, media };
}

describe("StudioProducerPanel", () => {
  it("stays out of the way until the compositor runs", () => {
    const { container } = renderPanel(composition({ isInitialized: false }));
    expect(container.querySelector(".fw-sc-producer")).toBeNull();
  });

  it("turns a picked image into an overlay", async () => {
    const { compositor, container } = renderPanel();
    const file = new File(["x"], "logo.png", { type: "image/png" });

    await pick(container.querySelector("input[accept='image/*']") as HTMLInputElement, file);

    expect(compositor.addImageOverlay).toHaveBeenCalledWith(file);
  });

  it("says so when a picked image cannot be read", async () => {
    const compositor = fakeCompositor();
    compositor.addImageOverlay.mockRejectedValue(new Error("bad image"));
    const { container } = renderPanel(composition(), compositor);

    await pick(
      container.querySelector("input[accept='image/*']") as HTMLInputElement,
      new File(["x"], "broken.png")
    );

    expect(await screen.findByText("This image could not be read.")).toBeTruthy();
  });

  it("adds a text overlay with the studio's default text", async () => {
    const { compositor } = renderPanel();

    await fireEvent.click(screen.getByText("Add text"));

    expect(compositor.addTextOverlay).toHaveBeenCalledWith({ text: "Your text" });
  });

  it("updates text, size and colour as they are edited", async () => {
    const { compositor } = renderPanel(composition({ overlays: [textOverlay] }));

    await fireEvent.input(screen.getByLabelText("Text"), { target: { value: "Grace" } });
    await fireEvent.input(screen.getByLabelText("Text size"), { target: { value: "72" } });
    await fireEvent.input(screen.getByLabelText("Text color"), { target: { value: "#ff0000" } });

    expect(compositor.updateTextOverlay).toHaveBeenCalledWith("text-1", { text: "Grace" });
    expect(compositor.updateTextOverlay).toHaveBeenCalledWith("text-1", { fontSize: 72 });
    expect(compositor.updateTextOverlay).toHaveBeenCalledWith("text-1", { color: "#ff0000" });
  });

  it("moves and sizes a logo, and removes it", async () => {
    const { compositor } = renderPanel(composition({ overlays: [imageOverlay] }));

    await fireEvent.input(screen.getByLabelText("Left to right"), { target: { value: "25" } });
    await fireEvent.input(screen.getByLabelText("Top to bottom"), { target: { value: "75" } });
    await fireEvent.input(screen.getByLabelText("Size"), { target: { value: "30" } });
    await fireEvent.click(screen.getByTitle("Remove overlay"));

    expect(compositor.setOverlayPlacement).toHaveBeenCalledWith("img-1", { x: 0.25 });
    expect(compositor.setOverlayPlacement).toHaveBeenCalledWith("img-1", { y: 0.75 });
    expect(compositor.setOverlayPlacement).toHaveBeenCalledWith("img-1", { size: 0.3 });
    expect(compositor.removeOverlay).toHaveBeenCalledWith("img-1");
  });

  it("lists media files without its own add button and says they stay open in this tab", () => {
    const { container } = renderPanel();

    expect(container.querySelector("input[accept='video/*,audio/*']")).toBeNull();
    expect(screen.getByText(/Keep this tab open/)).toBeTruthy();
  });

  it("adds a picked media file from the action bar button", async () => {
    const onAdd = vi.fn(async (_file: File) => ({}));
    const { container } = render(MediaFileButton, { props: { onAdd } });
    const file = new File(["x"], "clip.mp4", { type: "video/mp4" });

    await pick(
      container.querySelector("input[accept='video/*,audio/*']") as HTMLInputElement,
      file
    );

    expect(onAdd).toHaveBeenCalledWith(file);
  });

  it("says so when a media file cannot play", async () => {
    const onAdd = vi.fn(async (_file: File) => {
      throw new Error("unsupported");
    });
    const { container } = render(MediaFileButton, { props: { onAdd } });

    await pick(
      container.querySelector("input[accept='video/*,audio/*']") as HTMLInputElement,
      new File(["x"], "clip.xyz")
    );

    expect(await screen.findByText("This file could not be played in this browser.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Add media file" })).toBeTruthy();

    await fireEvent.mouseDown(document.body);

    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  });

  it("pauses a playing file and plays a paused one", async () => {
    const { media, unmount } = renderPanel(composition(), fakeCompositor(), [], [mediaSource()]);
    await fireEvent.click(screen.getByText("Pause"));
    expect(media.pauseMediaSource).toHaveBeenCalledWith("media-1");
    unmount();

    const paused = renderPanel(
      composition(),
      fakeCompositor(),
      [],
      [mediaSource({ playing: false })]
    );
    await fireEvent.click(screen.getByText("Play"));
    expect(paused.media.playMediaSource).toHaveBeenCalledWith("media-1");
  });

  it("turns looping on for a file", async () => {
    const { media } = renderPanel(composition(), fakeCompositor(), [], [mediaSource()]);

    await fireEvent.click(screen.getByLabelText("Loop"));

    expect(media.setMediaSourceLoop).toHaveBeenCalledWith("media-1", true);
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

  it("removes a layer from the active scene and offers its opacity", async () => {
    const { compositor } = renderPanel();

    await fireEvent.click(screen.getAllByTitle("Remove layer")[0]);

    expect(compositor.removeLayer).toHaveBeenCalledWith("one", "top");
    expect(screen.getAllByTitle("Edit opacity")).toHaveLength(2);
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

  it("selects a scene with a native button that holds no other control", () => {
    renderPanel();
    const select = screen.getByRole("button", { name: /Scene 2/ });

    expect(select.tagName).toBe("BUTTON");
    expect(select.getAttribute("aria-pressed")).toBe("false");
    expect(select.querySelector("button")).toBeNull();
    expect(screen.getByRole("button", { name: /Default/ }).getAttribute("aria-pressed")).toBe(
      "true"
    );
  });
});

describe("CompositorControls", () => {
  it("labels its sections in the studio locale", async () => {
    const { default: CompositorControls } =
      await import("../src/components/CompositorControls.svelte");
    render(CompositorControls, {
      props: {
        isEnabled: true,
        isInitialized: true,
        rendererType: "canvas2d",
        stats: null,
        sources: [],
        layers: [],
      },
      context: new Map([["fw-sc-translator", readable(createStudioTranslator({ locale: "fr" }))]]),
    });

    expect(screen.getByText("Disposition")).toBeTruthy();
  });
});
