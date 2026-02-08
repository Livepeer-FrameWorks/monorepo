import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  registerRenderer,
  createRenderer,
  getSupportedRenderers,
  getRecommendedRenderer,
  type CompositorRenderer,
} from "../src/core/renderers/index";

// ---------------------------------------------------------------------------
// Mock renderer classes
// ---------------------------------------------------------------------------

function makeMockRenderer(type: string, supported: boolean): new () => CompositorRenderer {
  return class MockRenderer implements CompositorRenderer {
    readonly type = type as any;
    readonly isSupported = supported;
    async init() {}
    renderScene() {}
    renderTransition() {}
    applyFilter() {}
    captureFrame(): any {
      return {};
    }
    resize() {}
    getStats() {
      return { fps: 0, frameTimeMs: 0 };
    }
    destroy() {}
  };
}

describe("Renderer Factory", () => {
  let origNavigator: PropertyDescriptor | undefined;
  let origWebGL2: any;

  beforeEach(() => {
    origNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
    origWebGL2 = (globalThis as any).WebGL2RenderingContext;
  });

  afterEach(() => {
    if (origNavigator) {
      Object.defineProperty(globalThis, "navigator", origNavigator);
    } else {
      delete (globalThis as any).navigator;
    }
    if (origWebGL2 !== undefined) {
      (globalThis as any).WebGL2RenderingContext = origWebGL2;
    } else {
      delete (globalThis as any).WebGL2RenderingContext;
    }
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // registerRenderer + createRenderer
  // ===========================================================================
  describe("registerRenderer and createRenderer", () => {
    it("createRenderer with auto returns first supported in fallback chain", () => {
      registerRenderer("canvas2d", makeMockRenderer("canvas2d", true));
      registerRenderer("webgl", makeMockRenderer("webgl", false));
      registerRenderer("webgpu", makeMockRenderer("webgpu", false));

      const renderer = createRenderer("auto");
      expect(renderer.type).toBe("canvas2d");
    });

    it("auto prefers webgpu when supported", () => {
      registerRenderer("canvas2d", makeMockRenderer("canvas2d", true));
      registerRenderer("webgl", makeMockRenderer("webgl", true));
      registerRenderer("webgpu", makeMockRenderer("webgpu", true));

      const renderer = createRenderer("auto");
      expect(renderer.type).toBe("webgpu");
    });

    it("auto falls back from webgpu to webgl", () => {
      registerRenderer("canvas2d", makeMockRenderer("canvas2d", true));
      registerRenderer("webgl", makeMockRenderer("webgl", true));
      registerRenderer("webgpu", makeMockRenderer("webgpu", false));

      const renderer = createRenderer("auto");
      expect(renderer.type).toBe("webgl");
    });

    it("specific preference falls back to webgl then canvas2d", () => {
      registerRenderer("canvas2d", makeMockRenderer("canvas2d", true));
      registerRenderer("webgl", makeMockRenderer("webgl", false));
      registerRenderer("webgpu", makeMockRenderer("webgpu", false));

      const renderer = createRenderer("webgpu");
      expect(renderer.type).toBe("canvas2d");
    });

    it("specific preference uses requested when supported", () => {
      registerRenderer("canvas2d", makeMockRenderer("canvas2d", true));
      registerRenderer("webgl", makeMockRenderer("webgl", true));
      registerRenderer("webgpu", makeMockRenderer("webgpu", false));

      const renderer = createRenderer("webgl");
      expect(renderer.type).toBe("webgl");
    });

    it("throws when no supported renderer available", () => {
      registerRenderer("canvas2d", makeMockRenderer("canvas2d", false));
      registerRenderer("webgl", makeMockRenderer("webgl", false));
      registerRenderer("webgpu", makeMockRenderer("webgpu", false));

      expect(() => createRenderer("auto")).toThrow("No supported renderer available");
    });
  });

  // ===========================================================================
  // getSupportedRenderers
  // ===========================================================================
  describe("getSupportedRenderers", () => {
    it("always includes canvas2d", () => {
      delete (globalThis as any).WebGL2RenderingContext;
      Object.defineProperty(globalThis, "navigator", {
        value: {},
        writable: true,
        configurable: true,
      });

      const supported = getSupportedRenderers();
      expect(supported).toContain("canvas2d");
    });

    it("includes webgl when WebGL2RenderingContext exists", () => {
      (globalThis as any).WebGL2RenderingContext = class {};
      Object.defineProperty(globalThis, "navigator", {
        value: {},
        writable: true,
        configurable: true,
      });

      const supported = getSupportedRenderers();
      expect(supported).toContain("canvas2d");
      expect(supported).toContain("webgl");
    });

    it("includes webgpu when navigator.gpu exists", () => {
      (globalThis as any).WebGL2RenderingContext = class {};
      Object.defineProperty(globalThis, "navigator", {
        value: { gpu: {} },
        writable: true,
        configurable: true,
      });

      const supported = getSupportedRenderers();
      expect(supported).toContain("canvas2d");
      expect(supported).toContain("webgl");
      expect(supported).toContain("webgpu");
    });

    it("excludes webgpu when navigator has no gpu", () => {
      delete (globalThis as any).WebGL2RenderingContext;
      Object.defineProperty(globalThis, "navigator", {
        value: {},
        writable: true,
        configurable: true,
      });

      const supported = getSupportedRenderers();
      expect(supported).not.toContain("webgpu");
    });
  });

  // ===========================================================================
  // getRecommendedRenderer
  // ===========================================================================
  describe("getRecommendedRenderer", () => {
    it("returns webgl when WebGL2 is available", () => {
      (globalThis as any).WebGL2RenderingContext = class {};
      Object.defineProperty(globalThis, "navigator", {
        value: {},
        writable: true,
        configurable: true,
      });

      expect(getRecommendedRenderer()).toBe("webgl");
    });

    it("returns canvas2d when WebGL2 is not available", () => {
      delete (globalThis as any).WebGL2RenderingContext;
      Object.defineProperty(globalThis, "navigator", {
        value: {},
        writable: true,
        configurable: true,
      });

      expect(getRecommendedRenderer()).toBe("canvas2d");
    });

    it("does not recommend webgpu even when available", () => {
      (globalThis as any).WebGL2RenderingContext = class {};
      Object.defineProperty(globalThis, "navigator", {
        value: { gpu: {} },
        writable: true,
        configurable: true,
      });

      expect(getRecommendedRenderer()).toBe("webgl");
    });
  });
});
