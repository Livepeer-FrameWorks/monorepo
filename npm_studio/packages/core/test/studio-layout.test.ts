import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SceneManager } from "../src/core/SceneManager";
import { parseStudioLayout, type StudioLayout } from "../src/core/studioLayout";

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
    return { width: this.width, height: this.height, close: vi.fn() };
  }
}

function manager() {
  const sm = new SceneManager({ width: 1920, height: 1080 });
  const internals = sm as unknown as { worker: unknown; workerReady: boolean };
  internals.worker = { postMessage: vi.fn(), terminate: vi.fn() };
  internals.workerReady = true;
  const scene = sm.createScene("Default");
  sm.setActiveScene(scene.id);
  return { sm, scene };
}

const decoded: Blob[] = [];

beforeEach(() => {
  decoded.length = 0;
  vi.spyOn(console, "log").mockImplementation(() => {});
  vi.spyOn(console, "warn").mockImplementation(() => {});
  vi.stubGlobal("OffscreenCanvas", FakeOffscreenCanvas);
  vi.stubGlobal(
    "createImageBitmap",
    vi.fn(async (source: Blob) => {
      decoded.push(source);
      return { width: 200, height: 100, close: vi.fn() };
    })
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("studio layout export and import", () => {
  it("carries scenes, overlays, their placement and the preset to a new studio", async () => {
    const { sm, scene } = manager();
    sm.addLayer(scene.id, "camera-1");
    const second = sm.createScene("Break", "#112233");
    const { sourceId: textId } = sm.addTextOverlay(scene.id, { text: "Ada", fontSize: 64 });
    sm.setOverlayPlacement(textId, { x: 0.1, y: 0.8 });
    const logo = new File([new Uint8Array([1, 2, 3])], "logo.png", { type: "image/png" });
    const { sourceId: logoId } = await sm.addImageOverlay(second.id, logo, { size: 0.2 });
    sm.addLayer(scene.id, logoId, undefined, { kind: "overlay" });
    sm.applyLayout({ mode: "pip-br", pipScale: 0.3 }, false);
    sm.setActiveScene(second.id);

    const layout = await sm.exportLayout();

    expect(layout).toMatchObject({
      version: 1,
      activeScene: 1,
      layout: { mode: "pip-br", pipScale: 0.3 },
      scenes: [
        { name: "Default", backgroundColor: "#000000" },
        { name: "Break", backgroundColor: "#112233" },
      ],
    });
    expect(layout.overlays).toEqual([
      expect.objectContaining({
        kind: "text",
        scenes: [0],
        x: 0.1,
        y: 0.8,
        text: expect.objectContaining({ text: "Ada", fontSize: 64 }),
      }),
      expect.objectContaining({
        kind: "image",
        scenes: [0, 1],
        label: "logo.png",
        size: 0.2,
        image: "data:image/png;base64,AQID",
      }),
    ]);
    // Camera layers belong to the session, not to the layout.
    expect(JSON.stringify(layout)).not.toContain("camera-1");

    const request = vi.fn();
    vi.stubGlobal("fetch", request);
    const { sm: restored } = manager();
    await restored.importLayout(JSON.parse(JSON.stringify(layout)) as StudioLayout);

    const scenes = restored.getAllScenes();
    expect(scenes.map((s) => [s.name, s.backgroundColor])).toEqual([
      ["Default", "#000000"],
      ["Break", "#112233"],
    ]);
    expect(restored.getActiveScene()?.name).toBe("Break");
    expect(restored.getCurrentLayout()).toMatchObject({ mode: "pip-br", pipScale: 0.3 });
    expect(restored.getOverlays(scenes[0].id).map((o) => o.kind)).toEqual(["text", "image"]);
    expect(restored.getOverlays(scenes[1].id)).toEqual([
      expect.objectContaining({ kind: "image", label: "logo.png", size: 0.2 }),
    ]);
    // The picked image came back from the data URL, without a request.
    expect(request).not.toHaveBeenCalled();
    expect(await decoded.at(-1)!.arrayBuffer()).toEqual(new Uint8Array([1, 2, 3]).buffer);

    expect(await restored.exportLayout()).toEqual(layout);
  });

  it("moves the active scene's sources into the imported active scene", async () => {
    const { sm, scene } = manager();
    sm.addLayer(scene.id, "camera-1");
    const hidden = sm.addLayer(scene.id, "screen-1");
    sm.setLayerVisibility(scene.id, hidden.id, false);
    sm.addTextOverlay(scene.id, { text: "Old" });

    await sm.importLayout({
      version: 1,
      layout: { mode: "solo" },
      activeScene: 0,
      scenes: [{ name: "Main", backgroundColor: "#000000" }],
      overlays: [],
    });

    const active = sm.getActiveScene()!;
    expect(sm.getAllScenes()).toHaveLength(1);
    expect(active.name).toBe("Main");
    expect(active.layers.map((l) => [l.sourceId, l.visible])).toEqual([
      ["camera-1", true],
      ["screen-1", false],
    ]);
    expect(sm.getOverlays(active.id)).toEqual([]);
  });

  it("leaves the studio as it was when the layout is not valid", async () => {
    const { sm, scene } = manager();
    sm.addTextOverlay(scene.id, { text: "Keep" });

    await expect(
      sm.importLayout({
        version: 1,
        layout: { mode: "solo" },
        activeScene: 3,
        scenes: [{ name: "Only", backgroundColor: "#000000" }],
        overlays: [],
      })
    ).rejects.toThrow("active scene");

    expect(sm.getAllScenes().map((s) => s.id)).toEqual([scene.id]);
    expect(sm.getOverlays(scene.id).map((o) => o.label)).toEqual(["Keep"]);
  });
});

describe("parseStudioLayout", () => {
  const valid = {
    version: 1,
    layout: { mode: "solo" },
    activeScene: 0,
    scenes: [{ name: "Main", backgroundColor: "#000000" }],
    overlays: [],
  };

  it("accepts a layout it wrote", () => {
    expect(parseStudioLayout(valid)).toEqual(valid);
  });

  it.each([
    ["another version", { ...valid, version: 2 }, "version"],
    ["no scenes", { ...valid, scenes: [] }, "no scenes"],
    [
      "an overlay in a scene it lacks",
      {
        ...valid,
        overlays: [{ kind: "text", scenes: [1], x: 0, y: 0, text: { text: "x" } }],
      },
      "names no scene",
    ],
    [
      "an image without a size",
      { ...valid, overlays: [{ kind: "image", scenes: [0], x: 0, y: 0, image: "a.png" }] },
      "no size",
    ],
  ])("refuses %s", (_name, input, message) => {
    expect(() => parseStudioLayout(input)).toThrow(message);
  });
});
