import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";

const controllers = vi.hoisted(
  () =>
    [] as {
      enableCompositor: ReturnType<typeof vi.fn>;
      disableCompositor: ReturnType<typeof vi.fn>;
      setEncoderOverrides: ReturnType<typeof vi.fn>;
      importLayout: ReturnType<typeof vi.fn>;
      exportLayout: ReturnType<typeof vi.fn>;
      renderer: ReturnType<typeof vi.fn>;
      emit: (event: string) => void;
    }[]
);
const sceneSwitches = vi.hoisted(() => vi.fn());

vi.mock("@livepeer-frameworks/streamcrafter-core", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();

  class FakeController {
    private sceneManager: unknown = null;
    private handlers = new Map<string, Set<() => void>>();
    rendererType = "auto";
    renderer = vi.fn((type: string) => {
      this.rendererType = type;
    });
    emit = (event: string) => this.handlers.get(event)?.forEach((handler) => handler());
    enableCompositor = vi.fn(async () => {
      if (this.sceneManager) return;
      const scene = { id: "scene-1", name: "Default", layers: [], backgroundColor: "#000000" };
      const second = { id: "scene-2", name: "Scene 2", layers: [], backgroundColor: "#000000" };
      this.sceneManager = {
        on: (event: string, handler: () => void) => {
          if (!this.handlers.has(event)) this.handlers.set(event, new Set());
          this.handlers.get(event)!.add(handler);
          return () => this.handlers.get(event)?.delete(handler);
        },
        getAllScenes: () => [scene, second],
        getActiveScene: () => scene,
        setActiveScene: sceneSwitches,
        getRendererType: () => this.rendererType,
        setRenderer: this.renderer,
        getStats: () => ({ fps: 30, frameTimeMs: 1 }),
        getCurrentLayout: () => ({ mode: "solo" }),
        getOverlays: () => [],
        isInitialized: () => true,
      };
    });
    disableCompositor = vi.fn(() => {
      this.sceneManager = null;
    });
    setEncoderOverrides = vi.fn();
    importLayout = vi.fn(async () => {});
    exportLayout = vi.fn(async () => (this.sceneManager ? { version: 1, scenes: [] } : null));
    constructor() {
      controllers.push(this);
    }
    on() {
      return () => {};
    }
    getSceneManager() {
      return this.sceneManager;
    }
    getAudioMixer() {
      return {
        on: () => () => {},
        startLevelMonitoring: () => {},
        stopLevelMonitoring: () => {},
      };
    }
    getMediaStream() {
      return null;
    }
    getSources() {
      return [];
    }
    destroy() {}
    getEncoderManager() {
      return null;
    }
    isWebCodecsActive() {
      return false;
    }
    isCompositorEnabled() {
      return this.sceneManager !== null;
    }
  }

  return { ...actual, IngestControllerV2: FakeController };
});

import StreamCrafter from "../src/StreamCrafter.svelte";
import ProducerPanelHost from "./fixtures/ProducerPanelHost.svelte";
import PreviewHost from "./fixtures/PreviewHost.svelte";

describe("StreamCrafter producer panel", () => {
  beforeEach(() => {
    controllers.length = 0;
    sceneSwitches.mockClear();
    vi.spyOn(console, "log").mockImplementation(() => {});
  });

  it("goes to the next scene on ]", async () => {
    const { container } = render(StreamCrafter, { whipUrl: "https://ingest.example/whip" });
    await screen.findByText("Scenes");

    await fireEvent.keyDown(container.querySelector(".fw-sc-root") as HTMLElement, { key: "]" });

    expect(sceneSwitches).toHaveBeenCalledWith("scene-2");
  });

  it("is on by default once the compositor runs", async () => {
    render(StreamCrafter, { whipUrl: "https://ingest.example/whip" });

    expect(await screen.findByText("Scenes")).toBeTruthy();
    expect(screen.getByText("Default")).toBeTruthy();
  });

  it("masks the stream key in the copied stream info", async () => {
    const writeText = vi.fn(async (_text: string) => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const { container } = render(StreamCrafter, {
      whipUrl: "https://edge.example/webrtc/secret-key",
    });
    await screen.findByText("Scenes");

    await fireEvent.contextMenu(container.querySelector(".fw-sc-root") as HTMLElement);
    await fireEvent.click(await screen.findByText("Copy Stream Info"));

    expect(writeText).toHaveBeenCalledTimes(1);
    const copied = writeText.mock.calls[0][0];
    expect(copied).toContain("WHIP: https://edge.example/webrtc/•••");
    expect(copied).not.toContain("secret-key");
  });

  it("can be turned off", async () => {
    const { container } = render(StreamCrafter, {
      whipUrl: "https://ingest.example/whip",
      showProducerPanel: false,
    });

    await waitFor(() => expect(controllers[0]?.enableCompositor).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve));

    expect(container.querySelector(".fw-sc-producer")).toBeNull();
  });

  it("stays away when the compositor is turned off", async () => {
    const { container } = render(StreamCrafter, {
      whipUrl: "https://ingest.example/whip",
      enableCompositor: false,
    });

    await waitFor(() => expect(controllers).toHaveLength(1));
    expect(container.querySelector(".fw-sc-producer")).toBeNull();
    expect(controllers[0].enableCompositor).not.toHaveBeenCalled();
  });

  it("gives composable children the compositor through context", async () => {
    render(ProducerPanelHost);

    expect(await screen.findByText("Default")).toBeTruthy();
  });

  it("shows the layout toolbar on a composable preview once the compositor runs", async () => {
    const { container } = render(PreviewHost);

    await waitFor(() => expect(container.querySelector(".fw-sc-layout-overlay")).toBeTruthy());
  });

  it("moves the compositor onto the new controller when the endpoint changes", async () => {
    const { rerender } = render(StreamCrafter, {
      whipUrl: "https://ingest.example/first",
    });
    await waitFor(() => expect(controllers[0]?.enableCompositor).toHaveBeenCalled());

    await rerender({ whipUrl: "https://ingest.example/second" });

    await waitFor(() => expect(controllers).toHaveLength(2));
    await waitFor(() => expect(controllers[1].enableCompositor).toHaveBeenCalled());
  });

  it("turns the compositor off and on again without a remount, and keeps the layout", async () => {
    const { container, rerender } = render(StreamCrafter, {
      whipUrl: "https://ingest.example/whip",
    });
    await screen.findByText("Scenes");
    const controller = controllers[0];

    await rerender({ enableCompositor: false });
    await waitFor(() => expect(controller.disableCompositor).toHaveBeenCalledTimes(1));
    expect(controller.exportLayout).toHaveBeenCalled();
    expect(container.querySelector(".fw-sc-producer")).toBeNull();

    await rerender({ enableCompositor: true });
    await waitFor(() => expect(controller.enableCompositor).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(controller.importLayout).toHaveBeenCalledWith({ version: 1, scenes: [] })
    );
    expect(await screen.findByText("Scenes")).toBeTruthy();
    expect(controllers).toHaveLength(1);
  });

  it("switches the renderer of the running compositor", async () => {
    const { rerender } = render(StreamCrafter, {
      whipUrl: "https://ingest.example/whip",
      compositorConfig: { renderer: "auto" },
    });
    await screen.findByText("Scenes");

    await rerender({ compositorConfig: { renderer: "webgl" } });

    await waitFor(() => expect(controllers[0].renderer).toHaveBeenCalledWith("webgl"));
    expect(controllers).toHaveLength(1);
    expect(controllers[0].enableCompositor).toHaveBeenCalledTimes(1);
  });

  it("starts from initialLayout and reports layout changes", async () => {
    const stored = {
      version: 1,
      layout: { mode: "solo" },
      activeScene: 0,
      scenes: [],
      overlays: [],
    };
    const onLayoutChange = vi.fn();
    render(StreamCrafter, {
      whipUrl: "https://ingest.example/whip",
      initialLayout: stored as never,
      onLayoutChange,
    });
    await screen.findByText("Scenes");
    expect(controllers[0].importLayout).toHaveBeenCalledWith(stored);

    controllers[0].emit("sceneCreated");

    await waitFor(() => expect(onLayoutChange).toHaveBeenCalledWith({ version: 1, scenes: [] }));
  });

  it("offers the encoder settings outside dev mode and hands them to the controller", async () => {
    render(StreamCrafter, { whipUrl: "https://ingest.example/whip" });
    await screen.findByText("Scenes");

    await fireEvent.click(screen.getByTitle("Settings"));
    const resolution = screen.getByLabelText("Resolution") as HTMLSelectElement;
    await fireEvent.change(resolution, { target: { value: "1280x720" } });

    expect(controllers[0].setEncoderOverrides).toHaveBeenLastCalledWith({
      video: { width: 1280, height: 720 },
    });
    expect((screen.getByLabelText("Resolution") as HTMLSelectElement).value).toBe("1280x720");

    await fireEvent.click(screen.getByText("Reset to Profile"));
    expect(controllers[0].setEncoderOverrides).toHaveBeenLastCalledWith({});
  });
});
