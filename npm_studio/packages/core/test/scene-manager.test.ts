import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { SceneManager } from "../src/core/SceneManager";

describe("SceneManager", () => {
  beforeEach(() => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Constructor / config
  // ===========================================================================
  describe("constructor", () => {
    it("creates with default config", () => {
      const sm = new SceneManager();
      expect(sm.getConfig()).toBeDefined();
      expect(sm.isInitialized()).toBe(false);
    });

    it("merges partial config", () => {
      const sm = new SceneManager({ width: 1280, height: 720 });
      const config = sm.getConfig();
      expect(config.width).toBe(1280);
      expect(config.height).toBe(720);
    });
  });

  // ===========================================================================
  // Scene CRUD
  // ===========================================================================
  describe("scene CRUD", () => {
    it("createScene returns scene with unique id", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");

      expect(scene.id).toBeDefined();
      expect(scene.name).toBe("Main");
      expect(scene.layers).toEqual([]);
      expect(scene.backgroundColor).toBe("#000000");
    });

    it("createScene with custom background color", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Green", "#00ff00");
      expect(scene.backgroundColor).toBe("#00ff00");
      expect(scene.name).toBe("Green");
      expect(scene.layers).toEqual([]);
    });

    it("createScene emits sceneCreated event", () => {
      const sm = new SceneManager();
      const handler = vi.fn();
      sm.on("sceneCreated", handler);

      const scene = sm.createScene("Test");
      expect(handler).toHaveBeenCalledWith({ scene });
    });

    it("getScene returns existing scene", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      expect(sm.getScene(scene.id)).toBe(scene);
    });

    it("getScene returns undefined for unknown id", () => {
      const sm = new SceneManager();
      sm.createScene("A");
      expect(sm.getScene("nonexistent")).toBeUndefined();
    });

    it("getAllScenes returns all scenes", () => {
      const sm = new SceneManager();
      sm.createScene("A");
      sm.createScene("B");
      expect(sm.getAllScenes()).toHaveLength(2);
    });

    it("deleteScene removes scene", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Temp");
      sm.deleteScene(scene.id);
      expect(sm.getScene(scene.id)).toBeUndefined();
    });

    it("deleteScene emits sceneDeleted event", () => {
      const sm = new SceneManager();
      const handler = vi.fn();
      sm.on("sceneDeleted", handler);

      const scene = sm.createScene("Temp");
      sm.deleteScene(scene.id);
      expect(handler).toHaveBeenCalledWith({ sceneId: scene.id });
    });

    it("deleteScene throws for unknown scene", () => {
      const sm = new SceneManager();
      expect(() => sm.deleteScene("fake")).toThrow("Scene not found");
    });

    it("deleteScene throws for active scene", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      sm.setActiveScene(scene.id);

      expect(() => sm.deleteScene(scene.id)).toThrow("Cannot delete the active scene");
    });
  });

  // ===========================================================================
  // Active scene
  // ===========================================================================
  describe("active scene", () => {
    it("getActiveScene returns undefined initially", () => {
      const sm = new SceneManager();
      sm.createScene("A");
      expect(sm.getActiveScene()).toBeUndefined();
      expect(sm.getAllScenes()).toHaveLength(1);
    });

    it("setActiveScene sets the active scene", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      sm.setActiveScene(scene.id);
      expect(sm.getActiveScene()).toBe(scene);
    });

    it("setActiveScene throws for unknown scene", () => {
      const sm = new SceneManager();
      expect(() => sm.setActiveScene("nope")).toThrow("Scene not found");
    });

    it("setActiveScene emits sceneActivated", () => {
      const sm = new SceneManager();
      const handler = vi.fn();
      sm.on("sceneActivated", handler);

      const scene = sm.createScene("Main");
      sm.setActiveScene(scene.id);

      expect(handler).toHaveBeenCalledWith({
        scene,
        previousSceneId: null,
      });
    });

    it("switching active scene sets previousSceneId", () => {
      const sm = new SceneManager();
      const handler = vi.fn();
      sm.on("sceneActivated", handler);

      const scene1 = sm.createScene("A");
      const scene2 = sm.createScene("B");

      sm.setActiveScene(scene1.id);
      expect(handler).toHaveBeenCalledTimes(1);
      expect(handler.mock.calls[0][0].previousSceneId).toBeNull();

      sm.setActiveScene(scene2.id);
      expect(handler).toHaveBeenCalledTimes(2);

      const lastCall = handler.mock.calls[1][0];
      expect(lastCall.scene.id).toBe(scene2.id);
      expect(lastCall.previousSceneId).toBe(scene1.id);
      expect(sm.getActiveScene()).toBe(scene2);
    });
  });

  // ===========================================================================
  // Layer management
  // ===========================================================================
  describe("layer management", () => {
    it("addLayer creates a layer with default transform", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      const layer = sm.addLayer(scene.id, "camera-1");

      expect(layer.id).toBeDefined();
      expect(layer.sourceId).toBe("camera-1");
      expect(layer.visible).toBe(true);
      expect(layer.locked).toBe(false);
      expect(layer.zIndex).toBe(0);
      expect(layer.transform).toBeDefined();
    });

    it("addLayer with custom transform", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      const layer = sm.addLayer(scene.id, "cam", { x: 0.5, y: 0.5, width: 0.25, height: 0.25 });

      expect(layer.transform.x).toBe(0.5);
      expect(layer.transform.y).toBe(0.5);
      expect(layer.transform.width).toBe(0.25);
    });

    it("addLayer increments zIndex", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      const l1 = sm.addLayer(scene.id, "cam1");
      const l2 = sm.addLayer(scene.id, "cam2");

      expect(l1.zIndex).toBe(0);
      expect(l2.zIndex).toBe(1);
    });

    it("addLayer emits layerAdded", () => {
      const sm = new SceneManager();
      const handler = vi.fn();
      sm.on("layerAdded", handler);

      const scene = sm.createScene("Main");
      const layer = sm.addLayer(scene.id, "cam");

      expect(handler).toHaveBeenCalledWith({ sceneId: scene.id, layer });
    });

    it("addLayer throws for unknown scene", () => {
      const sm = new SceneManager();
      expect(() => sm.addLayer("fake", "cam")).toThrow("Scene not found");
    });

    it("removeLayer removes the layer", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      const layer = sm.addLayer(scene.id, "cam");

      sm.removeLayer(scene.id, layer.id);
      expect(sm.getScene(scene.id)!.layers).toHaveLength(0);
    });

    it("removeLayer emits layerRemoved", () => {
      const sm = new SceneManager();
      const handler = vi.fn();
      sm.on("layerRemoved", handler);

      const scene = sm.createScene("Main");
      const layer = sm.addLayer(scene.id, "cam");
      sm.removeLayer(scene.id, layer.id);

      expect(handler).toHaveBeenCalledWith({ sceneId: scene.id, layerId: layer.id });
    });

    it("removeLayer throws for unknown scene", () => {
      const sm = new SceneManager();
      expect(() => sm.removeLayer("fake", "layer")).toThrow("Scene not found");
    });

    it("removeLayer throws for unknown layer", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      expect(() => sm.removeLayer(scene.id, "fake")).toThrow("Layer not found");
    });

    it("updateLayerTransform updates transform", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      const layer = sm.addLayer(scene.id, "cam");

      sm.updateLayerTransform(scene.id, layer.id, { x: 0.1, y: 0.2 });

      const updated = sm.getScene(scene.id)!.layers[0];
      expect(updated.transform.x).toBe(0.1);
      expect(updated.transform.y).toBe(0.2);
    });

    it("updateLayerTransform emits layerUpdated", () => {
      const sm = new SceneManager();
      const handler = vi.fn();
      sm.on("layerUpdated", handler);

      const scene = sm.createScene("Main");
      const layer = sm.addLayer(scene.id, "cam");
      sm.updateLayerTransform(scene.id, layer.id, { x: 0.5 });

      expect(handler).toHaveBeenCalledWith(expect.objectContaining({ sceneId: scene.id }));
    });

    it("updateLayerTransform throws for unknown scene", () => {
      const sm = new SceneManager();
      expect(() => sm.updateLayerTransform("fake", "layer", { x: 0 })).toThrow("Scene not found");
    });

    it("updateLayerTransform throws for unknown layer", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      expect(() => sm.updateLayerTransform(scene.id, "fake", { x: 0 })).toThrow("Layer not found");
    });

    it("setLayerVisibility toggles visible", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      const layer = sm.addLayer(scene.id, "cam");

      sm.setLayerVisibility(scene.id, layer.id, false);
      expect(sm.getScene(scene.id)!.layers[0].visible).toBe(false);

      sm.setLayerVisibility(scene.id, layer.id, true);
      expect(sm.getScene(scene.id)!.layers[0].visible).toBe(true);
    });

    it("reorderLayers changes layer zIndex values", () => {
      const sm = new SceneManager();
      const scene = sm.createScene("Main");
      const l1 = sm.addLayer(scene.id, "a");
      const l2 = sm.addLayer(scene.id, "b");
      const l3 = sm.addLayer(scene.id, "c");

      // Reverse order: l3 gets zIndex 0, l2 keeps 1, l1 gets 2
      sm.reorderLayers(scene.id, [l3.id, l2.id, l1.id]);

      const layers = sm.getScene(scene.id)!.layers;
      const findLayer = (id: string) => layers.find((l) => l.id === id)!;
      expect(findLayer(l3.id).zIndex).toBe(0);
      expect(findLayer(l2.id).zIndex).toBe(1);
      expect(findLayer(l1.id).zIndex).toBe(2);
    });
  });

  // ===========================================================================
  // Layout
  // ===========================================================================
  describe("layout", () => {
    it("getCurrentLayout returns default layout", () => {
      const sm = new SceneManager();
      expect(sm.getCurrentLayout()).toBeDefined();
    });
  });

  // ===========================================================================
  // Renderer
  // ===========================================================================
  describe("renderer", () => {
    it("getRendererType returns config renderer", () => {
      const sm = new SceneManager({ renderer: "webgl" });
      expect(sm.getRendererType()).toBe("webgl");
    });

    it("getStats returns stats object", () => {
      const sm = new SceneManager();
      const stats = sm.getStats();
      expect(stats).toEqual({ fps: 0, frameTimeMs: 0 });
    });
  });

  // ===========================================================================
  // Output config
  // ===========================================================================
  describe("updateOutputConfig", () => {
    it("updates width and height", () => {
      const sm = new SceneManager({ width: 1920, height: 1080 });
      const changed = sm.updateOutputConfig({ width: 1280, height: 720 });
      expect(changed).toBe(true);
      expect(sm.getConfig().width).toBe(1280);
      expect(sm.getConfig().height).toBe(720);
    });

    it("returns false when config unchanged", () => {
      const sm = new SceneManager({ width: 1920, height: 1080 });
      const changed = sm.updateOutputConfig({ width: 1920, height: 1080 });
      expect(changed).toBe(false);
    });
  });

  // ===========================================================================
  // isInitialized
  // ===========================================================================
  describe("isInitialized", () => {
    it("returns false before initialize", () => {
      const sm = new SceneManager();
      expect(sm.isInitialized()).toBe(false);
      expect(sm.getConfig()).toBeDefined();
    });
  });
});
