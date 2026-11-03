import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

const controllers = vi.hoisted(
  () =>
    [] as {
      enableCompositor: ReturnType<typeof vi.fn>;
      disableCompositor: ReturnType<typeof vi.fn>;
      setEncoderOverrides: ReturnType<typeof vi.fn>;
      importLayout: ReturnType<typeof vi.fn>;
      exportLayout: ReturnType<typeof vi.fn>;
      setWhipUrl: ReturnType<typeof vi.fn>;
      destroyed: boolean;
      renderer: ReturnType<typeof vi.fn>;
      liveLayout: { version: number; scenes: { name: string }[] };
      emit: (event: string) => void;
      config: { resolveWhipUrl?: (signal: AbortSignal) => Promise<string> };
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
    // Like the core controller, it keeps the layout the compositor had when it stopped.
    liveLayout = { version: 1, scenes: [{ name: `live-${controllers.length}` }] };
    private stoppedLayout: unknown = null;
    private stopCompositor() {
      if (this.sceneManager) this.stoppedLayout = this.liveLayout;
      this.sceneManager = null;
    }
    disableCompositor = vi.fn(() => this.stopCompositor());
    setEncoderOverrides = vi.fn();
    importLayout = vi.fn(async (layout: { version: number; scenes: { name: string }[] }) => {
      this.liveLayout = layout;
    });
    exportLayout = vi.fn(async () => (this.sceneManager ? this.liveLayout : this.stoppedLayout));
    setWhipUrl = vi.fn(async () => {});
    destroyed = false;
    isCompositorEnabled() {
      return this.sceneManager !== null;
    }
    constructor(public config: Record<string, unknown>) {
      controllers.push(this as never);
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
        setAudioProcessing: () => {},
      };
    }
    getMediaStream() {
      return null;
    }
    getSources() {
      return [];
    }
    setAudioProcessing() {}
    getAudioProcessing() {
      return {};
    }
    destroy() {
      this.destroyed = true;
      this.stopCompositor();
    }
    getEncoderManager() {
      return null;
    }
    isWebCodecsActive() {
      return false;
    }
  }

  return { ...actual, IngestControllerV2: FakeController };
});

import { createDefaultStudioLayout } from "@livepeer-frameworks/streamcrafter-core";
import { StreamCrafter } from "../src/components/StreamCrafter";
import { StudioProducerPanel } from "../src/components/StudioProducerPanel";
import { StudioOverlayControls } from "../src/components/StudioOverlayControls";
import { StudioSettings } from "../src/components/StudioSettings";

const WHIP_URL = "https://ingest.example/whip";

beforeEach(() => {
  controllers.length = 0;
  sceneSwitches.mockClear();
  vi.spyOn(console, "log").mockImplementation(() => {});
});

afterEach(cleanup);

describe("StreamCrafter producer panel", () => {
  it("is on by default once the compositor runs", async () => {
    render(<StreamCrafter whipUrl={WHIP_URL} />);

    expect(await screen.findByText("Scenes")).toBeTruthy();
    expect(screen.getByText("Default")).toBeTruthy();
  });

  it("goes to the next scene on ] and leaves keys typed into a field alone", async () => {
    const { container } = render(<StreamCrafter whipUrl={WHIP_URL} />);
    await screen.findByText("Scenes");
    const root = container.querySelector(".fw-sc-root") as HTMLElement;

    fireEvent.keyDown(container.querySelector("textarea, input") ?? root, { key: "]" });
    fireEvent.keyDown(root, { key: "]" });

    expect(sceneSwitches).toHaveBeenCalledTimes(1);
    expect(sceneSwitches).toHaveBeenCalledWith("scene-2");
  });

  it("can be turned off", async () => {
    const { container } = render(<StreamCrafter whipUrl={WHIP_URL} showProducerPanel={false} />);

    await waitFor(() => expect(controllers[0]?.enableCompositor).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve));

    expect(container.querySelector(".fw-sc-producer")).toBeNull();
  });

  it("gives composable children the compositor through context", async () => {
    render(
      <StreamCrafter whipUrl={WHIP_URL}>
        <StudioProducerPanel />
        <StudioOverlayControls />
      </StreamCrafter>
    );

    expect(await screen.findByText("Default")).toBeTruthy();
    expect(screen.getAllByText("Overlays")).toHaveLength(2);
  });

  it("masks the stream key in the copied stream info", async () => {
    const writeText = vi.fn(async (_text: string) => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const { container } = render(
      <StreamCrafter whipUrl="https://edge.example/webrtc/secret-key" />
    );
    await screen.findByText("Scenes");

    fireEvent.contextMenu(container.querySelector(".fw-sc-root") as HTMLElement);
    fireEvent.click(await screen.findByText("Copy Stream Info"));

    expect(writeText).toHaveBeenCalledTimes(1);
    const copied = writeText.mock.calls[0][0];
    expect(copied).toContain("WHIP: https://edge.example/webrtc/•••");
    expect(copied).not.toContain("secret-key");
  });

  it("hands resolveWhipUrl to the controller and calls the latest one", async () => {
    const first = vi.fn(async () => "https://edge.example/webrtc/first");
    const second = vi.fn(async () => "https://edge.example/webrtc/second");
    const { rerender } = render(<StreamCrafter whipUrl={WHIP_URL} resolveWhipUrl={first} />);
    await waitFor(() => expect(controllers).toHaveLength(1));
    rerender(<StreamCrafter whipUrl={WHIP_URL} resolveWhipUrl={second} />);

    const signal = new AbortController().signal;
    await expect(controllers[0].config.resolveWhipUrl?.(signal)).resolves.toBe(
      "https://edge.example/webrtc/second"
    );
    expect(second).toHaveBeenCalledWith(signal);
    expect(first).not.toHaveBeenCalled();
  });

  it("stays away when the compositor is turned off", async () => {
    const { container } = render(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} />);

    await waitFor(() => expect(controllers).toHaveLength(1));
    expect(container.querySelector(".fw-sc-producer")).toBeNull();
    expect(controllers[0].enableCompositor).not.toHaveBeenCalled();
  });

  it("turns the compositor off and on again without a new controller, and keeps the layout", async () => {
    const { rerender } = render(<StreamCrafter whipUrl={WHIP_URL} />);
    await screen.findByText("Scenes");
    const controller = controllers[0];

    rerender(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} />);
    await waitFor(() => expect(controller.disableCompositor).toHaveBeenCalledTimes(1));

    rerender(<StreamCrafter whipUrl={WHIP_URL} />);
    await waitFor(() => expect(controller.enableCompositor).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(controller.importLayout).toHaveBeenCalledWith(controller.liveLayout)
    );
    expect(controllers).toHaveLength(1);
  });

  it("sends a new endpoint to the running controller, which keeps its sources", async () => {
    const { rerender } = render(<StreamCrafter whipUrl={WHIP_URL} />);
    await screen.findByText("Scenes");

    rerender(<StreamCrafter whipUrl="https://ingest.example/whip-2" />);

    await waitFor(() =>
      expect(controllers[0].setWhipUrl).toHaveBeenCalledWith(
        "https://ingest.example/whip-2",
        undefined
      )
    );
    expect(controllers).toHaveLength(1);
    expect(controllers[0].destroyed).toBe(false);
    expect(controllers[0].enableCompositor).toHaveBeenCalledTimes(1);
  });

  it("hands the encoder choices to the controller for a new profile", async () => {
    const { rerender } = render(<StreamCrafter whipUrl={WHIP_URL} />);
    await screen.findByText("Scenes");
    fireEvent.click(screen.getByTitle("Settings"));
    fireEvent.change(screen.getByLabelText("Resolution"), { target: { value: "1280x720" } });

    rerender(<StreamCrafter whipUrl={WHIP_URL} initialProfile="conference" />);

    await waitFor(() => expect(controllers).toHaveLength(2));
    expect(controllers[1].setEncoderOverrides).toHaveBeenCalledWith({
      video: { width: 1280, height: 720 },
    });
  });

  it("carries the current layout to a new controller for the same stream", async () => {
    const stored = { version: 1, layout: { mode: "solo" }, activeScene: 0, scenes: [] };
    const { rerender } = render(
      <StreamCrafter whipUrl={WHIP_URL} layoutKey="stream-a" initialLayout={stored as never} />
    );
    await screen.findByText("Scenes");
    expect(controllers[0].importLayout).toHaveBeenCalledWith(stored);
    const edited = { ...stored, scenes: [{ name: "Edited" }] };
    controllers[0].liveLayout = edited as never;

    rerender(
      <StreamCrafter
        whipUrl={WHIP_URL}
        initialProfile="conference"
        layoutKey="stream-a"
        initialLayout={{ ...stored } as never}
      />
    );

    await waitFor(() => expect(controllers[1]?.importLayout).toHaveBeenCalledWith(edited));
    expect(controllers[1].importLayout).toHaveBeenCalledTimes(1);
  });

  it("shows another stream's layout on the running controller", async () => {
    const first = { version: 1, layout: { mode: "solo" }, activeScene: 0, scenes: [] };
    const second = { ...first, layout: { mode: "grid" } };
    const { rerender } = render(
      <StreamCrafter whipUrl={WHIP_URL} layoutKey="stream-a" initialLayout={first as never} />
    );
    await screen.findByText("Scenes");

    rerender(
      <StreamCrafter
        whipUrl="https://ingest.example/whip-2"
        layoutKey="stream-b"
        initialLayout={second as never}
      />
    );

    await waitFor(() => expect(controllers[0].importLayout).toHaveBeenLastCalledWith(second));
    expect(controllers).toHaveLength(1);
    expect(controllers[0].destroyed).toBe(false);
  });

  it("shows the default layout for another stream without one, never the previous scenes", async () => {
    const { rerender } = render(
      <StreamCrafter whipUrl={WHIP_URL} layoutKey="stream-a" initialLayout={null} />
    );
    await screen.findByText("Scenes");
    expect(controllers[0].importLayout).not.toHaveBeenCalled();

    rerender(<StreamCrafter whipUrl={WHIP_URL} layoutKey="stream-b" initialLayout={null} />);

    await waitFor(() =>
      expect(controllers[0].importLayout).toHaveBeenCalledWith(createDefaultStudioLayout())
    );
  });

  it("reports each layout with its key and drops one exported for the previous stream", async () => {
    const onLayoutChange = vi.fn();
    const { rerender } = render(
      <StreamCrafter
        whipUrl={WHIP_URL}
        layoutKey="stream-a"
        initialLayout={null}
        onLayoutChange={onLayoutChange}
      />
    );
    await screen.findByText("Scenes");
    await waitFor(() => expect(onLayoutChange).toHaveBeenCalled());
    onLayoutChange.mockClear();

    let finishExport!: () => void;
    const exportDone = new Promise<void>((resolve) => (finishExport = resolve));
    const aLayout = controllers[0].liveLayout;
    controllers[0].exportLayout.mockImplementationOnce(async () => {
      await exportDone;
      return aLayout;
    });
    const exports = controllers[0].exportLayout.mock.calls.length;
    act(() => controllers[0].emit("sceneCreated"));
    await waitFor(() =>
      expect(controllers[0].exportLayout.mock.calls.length).toBeGreaterThan(exports)
    );

    rerender(
      <StreamCrafter
        whipUrl={WHIP_URL}
        layoutKey="stream-b"
        initialLayout={null}
        onLayoutChange={onLayoutChange}
      />
    );
    finishExport();

    await waitFor(() =>
      expect(onLayoutChange).toHaveBeenCalledWith(createDefaultStudioLayout(), "stream-b")
    );
    expect(onLayoutChange).not.toHaveBeenCalledWith(expect.anything(), "stream-a");
  });

  it("shows the encoder settings of the profile StudioSettings is given", async () => {
    render(
      <StreamCrafter whipUrl={WHIP_URL}>
        <StudioSettings qualityProfile="conference" defaultOpen />
      </StreamCrafter>
    );

    expect(((await screen.findByLabelText("Resolution")) as HTMLSelectElement).value).toBe(
      "1280x720"
    );
  });

  it("switches the renderer of the running compositor", async () => {
    const { rerender } = render(
      <StreamCrafter whipUrl={WHIP_URL} compositorConfig={{ renderer: "auto" }} />
    );
    await screen.findByText("Scenes");

    rerender(<StreamCrafter whipUrl={WHIP_URL} compositorConfig={{ renderer: "webgl" }} />);

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
    render(
      <StreamCrafter
        whipUrl={WHIP_URL}
        initialLayout={stored as never}
        onLayoutChange={onLayoutChange}
      />
    );
    await screen.findByText("Scenes");
    expect(controllers[0].importLayout).toHaveBeenCalledWith(stored);

    act(() => controllers[0].emit("sceneCreated"));

    await waitFor(() => expect(onLayoutChange).toHaveBeenCalledWith(stored, undefined));
  });

  it("offers the encoder settings outside dev mode and hands them to the controller", async () => {
    render(<StreamCrafter whipUrl={WHIP_URL} />);
    await screen.findByText("Scenes");

    fireEvent.click(screen.getByTitle("Settings"));
    const resolution = screen.getByLabelText("Resolution") as HTMLSelectElement;
    fireEvent.change(resolution, { target: { value: "1280x720" } });

    expect(controllers[0].setEncoderOverrides).toHaveBeenLastCalledWith({
      video: { width: 1280, height: 720 },
    });
    expect((screen.getByLabelText("Resolution") as HTMLSelectElement).value).toBe("1280x720");

    fireEvent.click(screen.getByText("Reset to Profile"));
    expect(controllers[0].setEncoderOverrides).toHaveBeenLastCalledWith({});
  });
});
