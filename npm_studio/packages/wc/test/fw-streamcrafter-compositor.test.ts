import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
    disableCompositor = vi.fn(() => {
      this.sceneManager = null;
    });
    setEncoderOverrides = vi.fn();
    importLayout = vi.fn(async () => {});
    exportLayout = vi.fn(async () => (this.sceneManager ? { version: 1, scenes: [] } : null));
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
    destroy() {}
    getEncoderManager() {
      return null;
    }
    isWebCodecsActive() {
      return false;
    }
  }

  return { ...actual, IngestControllerV2: FakeController };
});

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
    expect(controller.exportLayout).toHaveBeenCalled();

    el.enableCompositor = true;
    await vi.waitFor(() => expect(controller.enableCompositor).toHaveBeenCalledTimes(2));
    await vi.waitFor(() =>
      expect(controller.importLayout).toHaveBeenCalledWith({ version: 1, scenes: [] })
    );
    expect(controllers).toHaveLength(1);
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

    await vi.waitFor(() => expect(onLayoutChange).toHaveBeenCalledWith({ version: 1, scenes: [] }));
    expect(events).toContainEqual({ version: 1, scenes: [] });
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
});
