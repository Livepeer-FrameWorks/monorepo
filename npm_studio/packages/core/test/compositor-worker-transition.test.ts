import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CompositorMainToWorker, Scene } from "../src/types";

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

const scene = (id: string): Scene => ({ id, layers: [], backgroundColor: "#000000" });
const posted: { type: string; sceneId?: string }[] = [];

async function worker() {
  vi.stubGlobal("self", {
    onmessage: null,
    postMessage: (message: { type: string }) => posted.push(message),
    setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
    clearTimeout: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
  });
  vi.stubGlobal(
    "createImageBitmap",
    vi.fn(async () => ({ close: vi.fn() }))
  );
  vi.resetModules();
  await import("../src/workers/compositor.worker");
  const send = (data: CompositorMainToWorker) =>
    (self as unknown as { onmessage(e: unknown): Promise<void> }).onmessage({ data });
  await send({
    type: "init",
    canvas: {} as OffscreenCanvas,
    config: { enabled: true, renderer: "canvas2d", frameRate: 200, width: 16, height: 9 },
  } as unknown as CompositorMainToWorker);
  await send({ type: "updateScene", scene: scene("from") });
  return send;
}

describe("compositor worker scene transitions", () => {
  beforeEach(() => {
    posted.length = 0;
    vi.clearAllMocks();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("plays the transition to the scene it was given and then lands on it", async () => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    const send = await worker();

    await send({
      type: "startTransition",
      transition: { type: "fade", durationMs: 30, easing: "linear" },
      toScene: scene("to"),
    });
    await vi.waitFor(() => expect(renderer.renderTransition).toHaveBeenCalled());
    await vi.waitFor(() =>
      expect(posted).toContainEqual({ type: "transitionComplete", sceneId: "to" })
    );

    await send({ type: "destroy" });
  });

  it("lands on the target at once for a cut", async () => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    const send = await worker();

    await send({
      type: "startTransition",
      transition: { type: "cut", durationMs: 0, easing: "linear" },
      toScene: scene("to"),
    });

    expect(posted).toContainEqual({ type: "transitionComplete", sceneId: "to" });
    expect(renderer.renderTransition).not.toHaveBeenCalled();
    await send({ type: "destroy" });
  });
});
