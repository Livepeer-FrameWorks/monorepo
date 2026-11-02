import React from "react";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import type {
  CameraChoice,
  MediaSource,
  Scene,
  StudioOverlay,
} from "@livepeer-frameworks/streamcrafter-core";
import { CameraPicker } from "../src/components/CameraPicker";
import { MediaFileButton } from "../src/components/MediaFileButton";
import { StudioProducerPanel } from "../src/components/StudioProducerPanel";
import { StudioI18nProvider } from "../src/context/StudioI18nContext";
import { useCompositor, type UseCompositorReturn } from "../src/hooks/useCompositor";

afterEach(cleanup);

// jsdom media elements cannot capture; the panel offers media files to those that can.
beforeAll(() => {
  Object.assign(HTMLMediaElement.prototype, { captureStream: () => null });
});
afterAll(() => {
  delete (HTMLMediaElement.prototype as Partial<HTMLMediaElement>).captureStream;
});

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
    overlays: [],
    addImageOverlay: vi.fn(async () => {}),
    addTextOverlay: vi.fn(),
    updateTextOverlay: vi.fn(),
    setOverlayPlacement: vi.fn(),
    removeOverlay: vi.fn(),
    ...overrides,
  } as unknown as UseCompositorReturn;
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

const mediaSource = (playing: boolean) =>
  ({
    id: "media-1",
    type: "media",
    label: "clip.mp4",
    media: { playing, loop: false, duration: 8, hasVideo: true, hasAudio: true },
  }) as MediaSource;

const front: CameraChoice = { deviceId: "front", label: "Front camera", inUse: true };
const rear: CameraChoice = { deviceId: "rear", label: "Rear camera", inUse: false };
const camera = { id: "camera-1", type: "camera" } as MediaSource;

describe("CameraPicker", () => {
  function renderPicker(sources: MediaSource[], choices: CameraChoice[]) {
    const onPick = vi.fn();
    render(<CameraPicker sources={sources} onPick={onPick} getChoices={async () => choices} />);
    return onPick;
  }

  it("stays available once a camera is live", () => {
    renderPicker([camera], [front, rear]);

    expect((screen.getByTitle("Add Camera") as HTMLButtonElement).disabled).toBe(false);
  });

  it("starts the only camera straight away", async () => {
    const onPick = renderPicker([], [rear]);

    await act(async () => {
      fireEvent.click(screen.getByTitle("Add Camera"));
    });

    expect(onPick).toHaveBeenCalledWith("rear");
  });

  it("lets the broadcaster pick a second camera", async () => {
    const onPick = renderPicker([camera], [front, rear]);

    await act(async () => {
      fireEvent.click(screen.getByTitle("Add Camera"));
    });
    fireEvent.click(screen.getByRole("menuitem", { name: /Rear camera/ }));

    expect(onPick).toHaveBeenCalledWith("rear");
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("says so when the cameras cannot be listed", async () => {
    const onPick = vi.fn();
    render(
      <CameraPicker
        sources={[]}
        onPick={onPick}
        getChoices={async () => {
          throw new Error("enumerateDevices not supported");
        }}
      />
    );

    await act(async () => {
      fireEvent.click(screen.getByTitle("Add Camera"));
    });

    expect(screen.getByRole("alert").textContent).toBe("Could not list the cameras");
    expect(onPick).not.toHaveBeenCalled();
  });

  it("clears that message on the next try", async () => {
    const getChoices = vi
      .fn()
      .mockRejectedValueOnce(new Error("enumerateDevices not supported"))
      .mockResolvedValueOnce([rear]);
    render(<CameraPicker sources={[]} onPick={vi.fn()} getChoices={getChoices} />);

    await act(async () => {
      fireEvent.click(screen.getByTitle("Add Camera"));
    });
    await act(async () => {
      fireEvent.click(screen.getByTitle("Add Camera"));
    });

    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows a camera already in use without offering it again", async () => {
    renderPicker([camera], [front, rear]);

    await act(async () => {
      fireEvent.click(screen.getByTitle("Add Camera"));
    });

    const item = screen.getByRole("menuitem", { name: /Front camera/ }) as HTMLButtonElement;
    expect(item.disabled).toBe(true);
    expect(item.textContent).toContain("Camera active");
  });
});

describe("StudioProducerPanel overlays and media", () => {
  it("turns a picked image into an overlay", async () => {
    const compositor = fakeCompositor();
    const { container } = render(<StudioProducerPanel compositor={compositor} sources={[]} />);
    const file = new File(["x"], "logo.png", { type: "image/png" });

    await act(async () => {
      fireEvent.change(container.querySelector("input[accept='image/*']")!, {
        target: { files: [file] },
      });
    });

    expect(compositor.addImageOverlay).toHaveBeenCalledWith(file);
  });

  it("says so when a picked image cannot be read", async () => {
    const compositor = fakeCompositor({
      addImageOverlay: vi.fn(async () => {
        throw new Error("bad image");
      }),
    });
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { container } = render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    await act(async () => {
      fireEvent.change(container.querySelector("input[accept='image/*']")!, {
        target: { files: [new File(["x"], "broken.png")] },
      });
    });

    expect(screen.getByText("This image could not be read.")).toBeTruthy();
  });

  it("adds a text overlay with the studio's default text", () => {
    const compositor = fakeCompositor();
    render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    fireEvent.click(screen.getByText("Add text"));

    expect(compositor.addTextOverlay).toHaveBeenCalledWith({ text: "Your text" });
  });

  it("updates text, size and colour as they are edited", () => {
    const compositor = fakeCompositor({ overlays: [textOverlay] });
    render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    fireEvent.change(screen.getByLabelText("Text"), { target: { value: "Grace" } });
    fireEvent.change(screen.getByLabelText("Text size"), { target: { value: "72" } });
    fireEvent.change(screen.getByLabelText("Text color"), { target: { value: "#ff0000" } });

    expect(compositor.updateTextOverlay).toHaveBeenCalledWith("text-1", { text: "Grace" });
    expect(compositor.updateTextOverlay).toHaveBeenCalledWith("text-1", { fontSize: 72 });
    expect(compositor.updateTextOverlay).toHaveBeenCalledWith("text-1", { color: "#ff0000" });
  });

  it("moves and sizes a logo, and removes it", () => {
    const compositor = fakeCompositor({ overlays: [imageOverlay] });
    render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    fireEvent.change(screen.getByLabelText("Left to right"), { target: { value: "25" } });
    fireEvent.change(screen.getByLabelText("Top to bottom"), { target: { value: "75" } });
    fireEvent.change(screen.getByLabelText("Size"), { target: { value: "30" } });
    fireEvent.click(screen.getByTitle("Remove overlay"));

    expect(compositor.setOverlayPlacement).toHaveBeenCalledWith("img-1", { x: 0.25 });
    expect(compositor.setOverlayPlacement).toHaveBeenCalledWith("img-1", { y: 0.75 });
    expect(compositor.setOverlayPlacement).toHaveBeenCalledWith("img-1", { size: 0.3 });
    expect(compositor.removeOverlay).toHaveBeenCalledWith("img-1");
  });

  it("adds a picked media file from its button and says the file stays open in this tab", async () => {
    const onAdd = vi.fn(async () => ({}));
    const { container } = render(<MediaFileButton onAdd={onAdd} />);
    const file = new File(["x"], "clip.mp4", { type: "video/mp4" });

    await act(async () => {
      fireEvent.change(container.querySelector("input[accept='video/*,audio/*']")!, {
        target: { files: [file] },
      });
    });
    expect(onAdd).toHaveBeenCalledWith(file);

    render(<StudioProducerPanel compositor={fakeCompositor()} sources={[]} media={fakeMedia()} />);
    expect(screen.getByText(/Keep this tab open/)).toBeTruthy();
    expect(screen.queryByText("Add media file")).toBeNull();
  });

  it("names the media file button and closes its error on a click elsewhere", async () => {
    const onAdd = vi.fn(async () => {
      throw new Error("unsupported");
    });
    const { container } = render(<MediaFileButton onAdd={onAdd} />);

    await act(async () => {
      fireEvent.change(container.querySelector("input[accept='video/*,audio/*']")!, {
        target: { files: [new File(["x"], "clip.xyz")] },
      });
    });
    expect(screen.getByRole("alert").textContent).toBe(
      "This file could not be played in this browser."
    );
    expect(screen.getByRole("button", { name: "Add media file" })).toBeTruthy();

    fireEvent.mouseDown(document.body);

    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("pauses a playing file, plays a paused one and turns looping on", () => {
    const media = fakeMedia();
    const { rerender } = render(
      <StudioProducerPanel
        compositor={fakeCompositor()}
        sources={[mediaSource(true)]}
        media={media}
      />
    );

    fireEvent.click(screen.getByText("Pause"));
    fireEvent.click(screen.getByLabelText("Loop"));
    rerender(
      <StudioProducerPanel
        compositor={fakeCompositor()}
        sources={[mediaSource(false)]}
        media={media}
      />
    );
    fireEvent.click(screen.getByText("Play"));

    expect(media.pauseMediaSource).toHaveBeenCalledWith("media-1");
    expect(media.setMediaSourceLoop).toHaveBeenCalledWith("media-1", true);
    expect(media.playMediaSource).toHaveBeenCalledWith("media-1");
  });
});

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

  it("removes a layer and edits its opacity in the active scene", () => {
    const compositor = fakeCompositor({
      removeLayer: vi.fn(),
      updateLayerTransform: vi.fn(),
    } as Partial<UseCompositorReturn>);
    render(<StudioProducerPanel compositor={compositor} sources={[]} />);

    fireEvent.click(screen.getAllByTitle("Remove layer")[0]);
    expect(compositor.removeLayer).toHaveBeenCalledWith("one", "top");

    expect(screen.getAllByTitle("Edit opacity")).toHaveLength(2);
  });

  it("selects a scene with a native button that holds no other control", () => {
    const compositor = fakeCompositor();
    render(<StudioProducerPanel compositor={compositor} sources={[]} />);
    const select = screen.getByRole("button", { name: /Scene 2/ });

    // A native button, so Enter and Space work and nothing interactive is nested in it.
    expect(select.tagName).toBe("BUTTON");
    expect(select.getAttribute("aria-pressed")).toBe("false");
    expect(select.querySelector("button")).toBeNull();
    fireEvent.click(select);

    expect(compositor.transitionTo).toHaveBeenCalledWith("two", expect.anything());
    expect(screen.getByRole("button", { name: /Default/ }).getAttribute("aria-pressed")).toBe(
      "true"
    );
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
      getOverlays: () => [],
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
