import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SceneManager } from "../src/core/SceneManager";
import { TextOverlaySource } from "../src/core/TextOverlaySource";

interface Posted {
  type: string;
  sourceId?: string;
  bitmap?: { width: number; height: number };
}

class FakeOffscreenCanvas {
  ctx = {
    font: "",
    fillStyle: "",
    textBaseline: "",
    textAlign: "",
    measureText: (text: string) => ({ width: text.length * 20 }),
    clearRect: vi.fn(),
    fillRect: vi.fn(),
    fillText: vi.fn(),
    beginPath: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    quadraticCurveTo: vi.fn(),
    closePath: vi.fn(),
    fill: vi.fn(),
  };
  constructor(
    public width: number,
    public height: number
  ) {}
  getContext() {
    return this.ctx;
  }
  transferToImageBitmap() {
    if (this.width === 0 || this.height === 0) {
      throw new DOMException("Cannot transfer an empty canvas", "InvalidStateError");
    }
    return { width: this.width, height: this.height, close: vi.fn() };
  }
}

function manager() {
  const sm = new SceneManager({ width: 1920, height: 1080 });
  const posted: Posted[] = [];
  const internals = sm as unknown as { worker: unknown; workerReady: boolean };
  internals.worker = {
    postMessage: (message: Posted) => posted.push(message),
    terminate: vi.fn(),
  };
  internals.workerReady = true;
  const scene = sm.createScene("Main");
  sm.setActiveScene(scene.id);
  return { sm, scene, posted };
}

beforeEach(() => {
  vi.spyOn(console, "log").mockImplementation(() => {});
  vi.stubGlobal("OffscreenCanvas", FakeOffscreenCanvas);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("TextOverlaySource", () => {
  it("renders a lower third as a band sized to its text, not a full-frame sheet", () => {
    const source = new TextOverlaySource("text-1", 1920, 1080);

    const bitmap = source.render({ text: "Ada Lovelace", fontSize: 48, style: "lower-third" });

    expect(bitmap.width).toBeLessThan(400);
    expect(bitmap.height).toBeLessThan(150);
  });

  it("keeps a long line inside the frame", () => {
    const source = new TextOverlaySource("text-1", 640, 360);

    expect(source.render({ text: "x".repeat(500) }).width).toBe(640);
  });

  it("gives each line of text its own height", () => {
    const source = new TextOverlaySource("text-1", 1920, 1080);

    const one = source.render({ text: "Ada", fontSize: 48 }).height;
    const two = source.render({ text: "Ada\nCountess of Lovelace", fontSize: 48 }).height;

    expect(two).toBeGreaterThan(one * 1.4);
  });

  it("renders an emptied text field without throwing", () => {
    const source = new TextOverlaySource("text-1", 1920, 1080);

    expect(() => source.render({ text: "" })).not.toThrow();
  });
});

describe("text overlays", () => {
  it("are overlay layers, which a layout leaves alone", () => {
    const { sm, scene } = manager();
    sm.addLayer(scene.id, "camera");
    const { layerId } = sm.addTextOverlay(scene.id, { text: "Ada" });
    const before = { ...sm.getScene(scene.id)!.layers.find((l) => l.id === layerId)!.transform };

    sm.applyLayout({ mode: "split-h" }, false);

    const layer = sm.getScene(scene.id)!.layers.find((l) => l.id === layerId)!;
    expect(layer.kind).toBe("overlay");
    expect(layer.transform).toEqual(before);
  });

  it("start as a lower third near the bottom of the frame, sized to the text", () => {
    const { sm, scene } = manager();

    const { layerId } = sm.addTextOverlay(scene.id, { text: "Ada" });

    const { transform } = sm.getScene(scene.id)!.layers.find((l) => l.id === layerId)!;
    expect(transform.height).toBeLessThan(0.25);
    expect(transform.width).toBeLessThan(0.5);
    expect(transform.y + transform.height).toBeGreaterThan(0.8);
  });

  it("resize their layer when the text changes and keep their placement", () => {
    const { sm, scene, posted } = manager();
    const { layerId, sourceId } = sm.addTextOverlay(scene.id, { text: "Ada" });
    sm.setOverlayPlacement(sourceId, { x: 1, y: 1 });
    posted.length = 0;

    sm.updateTextOverlay(sourceId, { text: "Ada Lovelace, Analyst", fontSize: 96 });

    const layer = sm.getScene(scene.id)!.layers.find((l) => l.id === layerId)!;
    expect(posted.map((message) => message.type)).toContain("sourceImage");
    expect(layer.transform.x + layer.transform.width).toBeCloseTo(1);
    expect(layer.transform.y + layer.transform.height).toBeCloseTo(1);
    expect(layer.label).toBe("Ada Lovelace, Analyst");
  });
});

describe("image overlays", () => {
  function stubBitmap(width: number, height: number) {
    const close = vi.fn();
    vi.stubGlobal(
      "createImageBitmap",
      vi.fn(
        async (_source: unknown, options?: { resizeWidth?: number; resizeHeight?: number }) => ({
          width: options?.resizeWidth ?? width,
          height: options?.resizeHeight ?? height,
          close,
        })
      )
    );
  }

  it("decode a picked file in the browser, without a request", async () => {
    stubBitmap(200, 100);
    const request = vi.fn();
    vi.stubGlobal("fetch", request);
    const { sm, scene } = manager();

    const { layerId } = await sm.addImageOverlay(
      scene.id,
      new File(["x"], "logo.png", { type: "image/png" })
    );

    expect(request).not.toHaveBeenCalled();
    const layer = sm.getScene(scene.id)!.layers.find((l) => l.id === layerId)!;
    expect(layer.kind).toBe("overlay");
    expect(layer.label).toBe("logo.png");
  });

  it("keep the picture's shape at every size", async () => {
    stubBitmap(400, 100);
    const { sm, scene } = manager();
    const { layerId, sourceId } = await sm.addImageOverlay(scene.id, new Blob(["x"]));

    sm.setOverlayPlacement(sourceId, { size: 0.5 });

    const { transform } = sm.getScene(scene.id)!.layers.find((l) => l.id === layerId)!;
    expect((transform.width * 1920) / (transform.height * 1080)).toBeCloseTo(4);
    expect(transform.width).toBeCloseTo(0.5);
  });

  it("stay inside the frame at the far end of their position range", async () => {
    stubBitmap(100, 100);
    const { sm, scene } = manager();
    const { layerId, sourceId } = await sm.addImageOverlay(scene.id, new Blob(["x"]));

    sm.setOverlayPlacement(sourceId, { x: 1, y: 1, size: 0.25 });

    const { transform } = sm.getScene(scene.id)!.layers.find((l) => l.id === layerId)!;
    expect(transform.x + transform.width).toBeCloseTo(1);
    expect(transform.y + transform.height).toBeCloseTo(1);
    expect(sm.getOverlays(scene.id)[0]).toMatchObject({ x: 1, y: 1, size: 0.25, kind: "image" });
  });

  it("shrink a picture larger than the output before it reaches the worker", async () => {
    stubBitmap(8000, 4000);
    const { sm, scene, posted } = manager();

    await sm.addImageOverlay(scene.id, new Blob(["x"]));

    const image = posted.find((message) => message.type === "sourceImage");
    expect(image?.bitmap).toMatchObject({ width: 1920, height: 960 });
  });

  it("leave the worker and every scene when removed", async () => {
    stubBitmap(100, 100);
    const { sm, scene, posted } = manager();
    const { sourceId } = await sm.addImageOverlay(scene.id, new Blob(["x"]));

    sm.removeOverlay(sourceId);

    expect(sm.getScene(scene.id)!.layers).toEqual([]);
    expect(sm.getOverlays(scene.id)).toEqual([]);
    expect(posted).toContainEqual({ type: "removeSource", sourceId });
  });
});

describe("overlay stacking", () => {
  it("keeps an overlay above a source added after it", () => {
    const { sm, scene } = manager();
    const overlay = sm.addLayer(scene.id, "logo", {}, { kind: "overlay" });
    const camera = sm.addLayer(scene.id, "camera");

    const layers = sm.getScene(scene.id)!.layers;
    const zIndex = (id: string) => layers.find((layer) => layer.id === id)!.zIndex;
    expect(zIndex(overlay.id)).toBeGreaterThan(zIndex(camera.id));
  });

  it("fits an overlay to its box whatever scaling the layout uses", () => {
    const { sm, scene } = manager();
    sm.applyLayout({ mode: "solo", scalingMode: "crop" }, false);

    const overlay = sm.addLayer(scene.id, "logo", {}, { kind: "overlay" });

    expect(overlay.scalingMode).toBe("letterbox");
  });

  it("adds a persistent overlay to new scenes as an overlay", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ blob: async () => new Blob(["x"]) }))
    );
    vi.stubGlobal(
      "createImageBitmap",
      vi.fn(async () => ({ width: 10, height: 10, close: vi.fn() }))
    );
    const { sm } = manager();

    await sm.addPersistentOverlay("https://cdn.example/logo.png");
    const next = sm.createScene("Next");

    expect(next.layers[0].kind).toBe("overlay");
  });
});
