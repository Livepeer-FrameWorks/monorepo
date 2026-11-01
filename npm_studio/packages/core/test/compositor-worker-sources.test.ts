import { afterEach, describe, expect, it, vi } from "vitest";
import type { CompositorMainToWorker, Layer, Scene } from "../src/types";

const renderer = vi.hoisted(() => ({
  init: vi.fn(async () => {}),
  renderScene: vi.fn(),
  renderTransition: vi.fn(),
  resize: vi.fn(),
  getStats: vi.fn(() => ({ fps: 30, frameTimeMs: 1 })),
  destroy: vi.fn(),
}));

vi.mock("../src/core/renderers", () => ({ createRenderer: () => renderer }));
vi.mock("../src/core/renderers/Canvas2DRenderer", () => ({ Canvas2DRenderer: class {} }));
vi.mock("../src/core/renderers/WebGLRenderer", () => ({ WebGLRenderer: class {} }));
vi.mock("../src/core/renderers/WebGPURenderer", () => ({ WebGPURenderer: class {} }));

function layer(sourceId: string): Layer {
  return {
    id: `layer-${sourceId}`,
    sourceId,
    visible: true,
    locked: false,
    zIndex: 0,
    scalingMode: "letterbox",
    transform: {
      x: 0,
      y: 0,
      width: 1,
      height: 1,
      opacity: 1,
      rotation: 0,
      borderRadius: 0,
      crop: { top: 0, right: 0, bottom: 0, left: 0 },
    },
  };
}

const scene = (layers: Layer[]): Scene => ({
  id: "main",
  name: "Main",
  layers,
  backgroundColor: "#000000",
});

async function worker() {
  vi.stubGlobal("self", {
    onmessage: null,
    postMessage: vi.fn(),
    setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
    clearTimeout: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
  });
  vi.resetModules();
  await import("../src/workers/compositor.worker");
  const send = (data: CompositorMainToWorker) =>
    (self as unknown as { onmessage(e: unknown): Promise<void> }).onmessage({ data });
  await send({
    type: "init",
    canvas: {} as OffscreenCanvas,
    config: { enabled: true, renderer: "canvas2d", frameRate: 200, width: 16, height: 9 },
  } as unknown as CompositorMainToWorker);
  return send;
}

describe("compositor worker sources", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("closes a removed source's last frame and stops offering it to the renderer", async () => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    const send = await worker();
    const frame = { close: vi.fn() } as unknown as VideoFrame;
    await send({ type: "sourceFrame", sourceId: "camera", frame });

    await send({ type: "removeSource", sourceId: "camera" } as CompositorMainToWorker);
    renderer.renderScene.mockClear();

    await vi.waitFor(() => expect(renderer.renderScene).toHaveBeenCalled());
    expect(frame.close).toHaveBeenCalled();
    const frames = renderer.renderScene.mock.lastCall![1] as Map<string, VideoFrame>;
    expect(frames.has("camera")).toBe(false);
    await send({ type: "destroy" });
  });

  it("keeps a scene update that arrives during a layout animation", async () => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    const send = await worker();
    await send({ type: "updateScene", scene: scene([layer("camera")]) });

    await send({
      type: "animateLayout",
      targetScene: scene([layer("camera")]),
      transition: { durationMs: 20, easing: "linear" },
    });
    await send({ type: "updateScene", scene: scene([layer("camera"), layer("logo")]) });

    await vi.waitFor(() => {
      const rendered = renderer.renderScene.mock.lastCall![0] as Scene;
      expect(rendered.layers.map((l) => l.sourceId)).toEqual(["camera", "logo"]);
    });
    await new Promise((resolve) => setTimeout(resolve, 40));
    renderer.renderScene.mockClear();
    await vi.waitFor(() => expect(renderer.renderScene).toHaveBeenCalled());
    const settled = renderer.renderScene.mock.lastCall![0] as Scene;
    expect(settled.layers.map((l) => l.sourceId)).toEqual(["camera", "logo"]);
    await send({ type: "destroy" });
  });
});
