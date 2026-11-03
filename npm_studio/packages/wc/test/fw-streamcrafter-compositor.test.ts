import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
    }[]
);

// While set, starting a compositor waits for it.
const enableGate = vi.hoisted(() => ({ wait: null as Promise<void> | null }));

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
      if (enableGate.wait) await enableGate.wait;
      if (this.sceneManager) return;
      const scene = { id: "scene-1", name: "Default", layers: [], backgroundColor: "#000000" };
      this.sceneManager = {
        on: (event: string, handler: () => void) => {
          if (!this.handlers.has(event)) this.handlers.set(event, new Set());
          this.handlers.get(event)!.add(handler);
          return () => this.handlers.get(event)?.delete(handler);
        },
        getAllScenes: () => [scene],
        getActiveScene: () => scene,
        setActiveScene: () => {},
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
    constructor() {
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
    getReconnectionManager() {
      return { getState: () => null };
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
import "../src/components/fw-streamcrafter.js";
import type { FwStreamCrafter } from "../src/components/fw-streamcrafter.js";

let element: FwStreamCrafter | null = null;

async function studio(props: Partial<FwStreamCrafter> = {}) {
  const el = document.createElement("fw-streamcrafter") as FwStreamCrafter;
  el.whipUrl = "https://ingest.example/whip";
  Object.assign(el, props);
  document.body.appendChild(el);
  element = el;
  await el.updateComplete;
  await vi.waitFor(() => expect(controllers[0]?.enableCompositor).toHaveBeenCalled());
  return el;
}

beforeEach(() => {
  controllers.length = 0;
  enableGate.wait = null;
  vi.spyOn(console, "log").mockImplementation(() => {});
});

afterEach(() => {
  element?.remove();
  element = null;
});

describe("fw-streamcrafter compositor", () => {
  it("turns off and on again on the same controller, and keeps the layout", async () => {
    const el = await studio();
    const controller = controllers[0];

    el.enableCompositor = false;
    await vi.waitFor(() => expect(controller.disableCompositor).toHaveBeenCalledTimes(1));

    el.enableCompositor = true;
    await vi.waitFor(() => expect(controller.enableCompositor).toHaveBeenCalledTimes(2));
    await vi.waitFor(() =>
      expect(controller.importLayout).toHaveBeenCalledWith(controller.liveLayout)
    );
    expect(controllers).toHaveLength(1);
  });

  it("carries the current layout to a new controller for the same stream", async () => {
    const stored = { version: 1, layout: { mode: "solo" }, activeScene: 0, scenes: [] };
    const el = await studio({ layoutKey: "stream-a", initialLayout: stored as never });
    await vi.waitFor(() => expect(controllers[0].importLayout).toHaveBeenCalledWith(stored));
    await vi.waitFor(() => expect(el.sceneManager).not.toBeNull());
    const edited = { ...stored, scenes: [{ name: "Edited" }] };
    controllers[0].liveLayout = edited as never;

    el.initialLayout = { ...stored } as never;
    el.initialProfile = "conference";

    await vi.waitFor(() => expect(controllers[1]?.importLayout).toHaveBeenCalledWith(edited));
    expect(controllers[1].importLayout).toHaveBeenCalledTimes(1);
  });

  it("sends a new endpoint to the running controller, which keeps its sources", async () => {
    const el = await studio();
    await vi.waitFor(() => expect(el.sceneManager).not.toBeNull());

    el.whipUrl = "https://ingest.example/whip-2";
    await el.updateComplete;

    expect(controllers[0].setWhipUrl).toHaveBeenCalledWith("https://ingest.example/whip-2");
    expect(controllers).toHaveLength(1);
    expect(controllers[0].destroyed).toBe(false);
    expect(controllers[0].enableCompositor).toHaveBeenCalledTimes(1);
  });

  it("shows another stream's layout on the running controller", async () => {
    const first = { version: 1, layout: { mode: "solo" }, activeScene: 0, scenes: [] };
    const second = { ...first, layout: { mode: "grid" } };
    const el = await studio({ layoutKey: "stream-a", initialLayout: first as never });
    await vi.waitFor(() => expect(el.sceneManager).not.toBeNull());

    el.layoutKey = "stream-b";
    el.initialLayout = second as never;
    el.whipUrl = "https://ingest.example/whip-2";

    await vi.waitFor(() => expect(controllers[0].importLayout).toHaveBeenLastCalledWith(second));
    expect(controllers).toHaveLength(1);
    expect(controllers[0].destroyed).toBe(false);
  });

  it("shows the default layout for another stream without one, never the previous scenes", async () => {
    const el = await studio({ layoutKey: "stream-a", initialLayout: null });
    await vi.waitFor(() => expect(el.sceneManager).not.toBeNull());
    expect(controllers[0].importLayout).not.toHaveBeenCalled();

    el.layoutKey = "stream-b";

    await vi.waitFor(() =>
      expect(controllers[0].importLayout).toHaveBeenCalledWith(createDefaultStudioLayout())
    );
  });

  it("reports each layout with its key and drops one exported for the previous stream", async () => {
    const onLayoutChange = vi.fn();
    const el = await studio({ layoutKey: "stream-a", onLayoutChange });
    await vi.waitFor(() => expect(el.sceneManager).not.toBeNull());
    await el.updateComplete;
    const events: unknown[] = [];
    el.addEventListener("fw-sc-layout-change", (e) =>
      events.push((e as CustomEvent<{ layoutKey: unknown }>).detail.layoutKey)
    );

    let finishExport!: () => void;
    const exportDone = new Promise<void>((resolve) => (finishExport = resolve));
    const aLayout = controllers[0].liveLayout;
    controllers[0].exportLayout.mockImplementationOnce(async () => {
      await exportDone;
      return aLayout;
    });
    const exports = controllers[0].exportLayout.mock.calls.length;
    controllers[0].emit("sceneCreated");
    await vi.waitFor(() =>
      expect(controllers[0].exportLayout.mock.calls.length).toBeGreaterThan(exports)
    );

    el.layoutKey = "stream-b";
    await el.updateComplete;
    finishExport();

    await vi.waitFor(() =>
      expect(onLayoutChange).toHaveBeenCalledWith(createDefaultStudioLayout(), "stream-b")
    );
    expect(onLayoutChange).not.toHaveBeenCalledWith(expect.anything(), "stream-a");
    expect(events).not.toContain("stream-a");
  });

  it("starts the compositor on a controller built while the last one was starting", async () => {
    let release = () => {};
    enableGate.wait = new Promise<void>((resolve) => (release = resolve));
    const el = await studio();

    el.initialProfile = "conference";
    await el.updateComplete;
    release();

    await vi.waitFor(() => expect(controllers[1]?.enableCompositor).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(el.sceneManager).not.toBeNull());
  });

  it("switches the renderer of the running compositor", async () => {
    const el = await studio({ compositorConfig: { renderer: "auto" } });
    await vi.waitFor(() => expect(el.sceneManager).not.toBeNull());

    el.compositorConfig = { renderer: "webgl" };
    await el.updateComplete;

    expect(controllers[0].renderer).toHaveBeenCalledWith("webgl");
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
    const events: unknown[] = [];
    const el = await studio({ initialLayout: stored as never, onLayoutChange });
    el.addEventListener("fw-sc-layout-change", (e) =>
      events.push((e as CustomEvent<{ layout: unknown }>).detail.layout)
    );
    await vi.waitFor(() => expect(controllers[0].importLayout).toHaveBeenCalledWith(stored));
    await vi.waitFor(() => expect(el.sceneManager).not.toBeNull());
    await el.updateComplete;

    controllers[0].emit("sceneCreated");

    await vi.waitFor(() => expect(onLayoutChange).toHaveBeenCalledWith(stored, undefined));
    expect(events).toContainEqual(stored);
  });

  it("offers the encoder settings outside dev mode and hands them to the controller", async () => {
    const el = await studio();
    const root = el.shadowRoot!;

    root.querySelector<HTMLButtonElement>(".fw-sc-action-secondary--settings")!.click();
    await el.updateComplete;
    const resolution = [...root.querySelectorAll<HTMLLabelElement>(".fw-sc-encoder-row")]
      .find((row) => row.textContent?.includes("Resolution"))!
      .querySelector("select")!;
    resolution.value = "1280x720";
    resolution.dispatchEvent(new Event("change"));
    await el.updateComplete;

    expect(controllers[0].setEncoderOverrides).toHaveBeenLastCalledWith({
      video: { width: 1280, height: 720 },
    });

    root.querySelector<HTMLButtonElement>(".fw-sc-encoder-reset")!.click();
    expect(controllers[0].setEncoderOverrides).toHaveBeenLastCalledWith({});
  });

  it("hands the encoder choices to the controller for a new profile", async () => {
    const el = await studio();
    el.setEncoderOverrides({ video: { width: 1280, height: 720 } });

    el.initialProfile = "conference";
    await el.updateComplete;

    expect(controllers[1].setEncoderOverrides).toHaveBeenCalledWith({
      video: { width: 1280, height: 720 },
    });
  });
});
