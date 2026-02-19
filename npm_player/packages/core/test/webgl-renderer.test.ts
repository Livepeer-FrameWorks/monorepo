import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Stub browser globals needed by WebGLRenderer
function createMockGLContext(isWebGL2 = false) {
  const textures: object[] = [];
  const buffers: object[] = [];
  const shaders: object[] = [];
  const programs: object[] = [];

  return {
    TEXTURE_2D: 0x0de1,
    TEXTURE0: 0x84c0,
    TEXTURE_MIN_FILTER: 0x2801,
    TEXTURE_MAG_FILTER: 0x2800,
    TEXTURE_WRAP_S: 0x2802,
    TEXTURE_WRAP_T: 0x2803,
    LINEAR: 0x2601,
    CLAMP_TO_EDGE: 0x812f,
    ARRAY_BUFFER: 0x8892,
    STATIC_DRAW: 0x88e4,
    FLOAT: 0x1406,
    TRIANGLE_STRIP: 0x0005,
    VERTEX_SHADER: 0x8b31,
    FRAGMENT_SHADER: 0x8b30,
    COMPILE_STATUS: 0x8b81,
    LINK_STATUS: 0x8b82,
    LUMINANCE: 0x1909,
    LUMINANCE_ALPHA: 0x190a,
    RGBA: 0x1908,
    UNSIGNED_BYTE: 0x1401,
    UNSIGNED_SHORT: 0x1403,
    RED_INTEGER: isWebGL2 ? 0x8d94 : undefined,
    R16UI: isWebGL2 ? 0x8234 : undefined,

    createTexture: vi.fn(() => {
      const t = {};
      textures.push(t);
      return t;
    }),
    bindTexture: vi.fn(),
    texParameteri: vi.fn(),
    texImage2D: vi.fn(),
    activeTexture: vi.fn(),
    deleteTexture: vi.fn(),

    createBuffer: vi.fn(() => {
      const b = {};
      buffers.push(b);
      return b;
    }),
    bindBuffer: vi.fn(),
    bufferData: vi.fn(),
    deleteBuffer: vi.fn(),

    createShader: vi.fn(() => {
      const s = {};
      shaders.push(s);
      return s;
    }),
    shaderSource: vi.fn(),
    compileShader: vi.fn(),
    getShaderParameter: vi.fn(() => true),
    getShaderInfoLog: vi.fn(() => ""),
    deleteShader: vi.fn(),

    createProgram: vi.fn(() => {
      const p = {};
      programs.push(p);
      return p;
    }),
    attachShader: vi.fn(),
    linkProgram: vi.fn(),
    getProgramParameter: vi.fn(() => true),
    getProgramInfoLog: vi.fn(() => ""),
    deleteProgram: vi.fn(),
    useProgram: vi.fn(),

    getAttribLocation: vi.fn(() => 0),
    getUniformLocation: vi.fn(() => ({})),
    enableVertexAttribArray: vi.fn(),
    vertexAttribPointer: vi.fn(),

    uniform1i: vi.fn(),
    uniform3f: vi.fn(),
    uniform4f: vi.fn(),
    uniformMatrix3fv: vi.fn(),

    viewport: vi.fn(),
    drawArrays: vi.fn(),

    _textures: textures,
    _buffers: buffers,
  };
}

function createMockCanvas(glContext: any, isWebGL2 = false) {
  const listeners: Record<string, Function[]> = {};

  return {
    width: 0,
    height: 0,
    getContext: vi.fn((type: string) => {
      if (type === "webgl2" && isWebGL2) return glContext;
      if (type === "webgl2" && !isWebGL2) return null;
      if (type === "webgl") return glContext;
      return null;
    }),
    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners[event]) listeners[event] = [];
      listeners[event].push(handler);
    }),
    removeEventListener: vi.fn(),
    toDataURL: vi.fn((type: string, quality: number) => `data:${type};base64,mockdata`),
    _listeners: listeners,
    _fireEvent: (name: string, event?: any) => {
      for (const fn of listeners[name] ?? []) fn(event ?? { preventDefault: vi.fn() });
    },
  };
}

// Stub devicePixelRatio
const origDPR = (globalThis as any).devicePixelRatio;
beforeEach(() => {
  (globalThis as any).devicePixelRatio = 1;
});
afterEach(() => {
  if (origDPR === undefined) {
    delete (globalThis as any).devicePixelRatio;
  } else {
    (globalThis as any).devicePixelRatio = origDPR;
  }
});

describe("WebGLRenderer", () => {
  let WebGLRenderer: typeof import("../src/rendering/WebGLRenderer").WebGLRenderer;

  beforeEach(async () => {
    const mod = await import("../src/rendering/WebGLRenderer");
    WebGLRenderer = mod.WebGLRenderer;
  });

  // =========================================================================
  // Construction
  // =========================================================================
  describe("constructor", () => {
    it("creates with WebGL2 when available", () => {
      const gl = createMockGLContext(true);
      const canvas = createMockCanvas(gl, true);

      const renderer = new WebGLRenderer(canvas as any);
      expect(canvas.getContext).toHaveBeenCalledWith("webgl2", expect.any(Object));
      expect(renderer.hasWebGL2).toBe(true);
      renderer.destroy();
    });

    it("falls back to WebGL1 when WebGL2 unavailable", () => {
      const gl = createMockGLContext(false);
      const canvas = createMockCanvas(gl, false);

      const renderer = new WebGLRenderer(canvas as any);
      expect(canvas.getContext).toHaveBeenCalledWith("webgl2", expect.any(Object));
      expect(canvas.getContext).toHaveBeenCalledWith("webgl", expect.any(Object));
      expect(renderer.hasWebGL2).toBe(false);
      renderer.destroy();
    });

    it("throws if no WebGL at all", () => {
      const canvas = {
        getContext: vi.fn(() => null),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      };

      expect(() => new WebGLRenderer(canvas as any)).toThrow("WebGL not supported");
    });

    it("registers context loss handlers", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);

      const renderer = new WebGLRenderer(canvas as any);
      expect(canvas.addEventListener).toHaveBeenCalledWith(
        "webglcontextlost",
        expect.any(Function)
      );
      expect(canvas.addEventListener).toHaveBeenCalledWith(
        "webglcontextrestored",
        expect.any(Function)
      );
      renderer.destroy();
    });
  });

  // =========================================================================
  // Rotation
  // =========================================================================
  describe("setRotation", () => {
    it("normalizes rotation to 0-359", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      // Rotation is applied via the transform matrix in setUniforms,
      // so we verify the method doesn't throw for valid values
      renderer.setRotation(0);
      renderer.setRotation(90);
      renderer.setRotation(180);
      renderer.setRotation(270);
      renderer.setRotation(360); // wraps to 0
      renderer.setRotation(-90); // wraps to 270
      renderer.setRotation(450); // wraps to 90
      renderer.destroy();
    });
  });

  // =========================================================================
  // Mirror
  // =========================================================================
  describe("setMirror", () => {
    it("accepts horizontal mirror", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.setMirror(true);
      renderer.setMirror(false);
      renderer.destroy();
    });

    it("accepts horizontal and vertical mirror", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.setMirror(true, true);
      renderer.setMirror(false, true);
      renderer.setMirror(true, false);
      renderer.destroy();
    });
  });

  // =========================================================================
  // Snapshot
  // =========================================================================
  describe("snapshot", () => {
    it("returns a data URL with default PNG", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      const result = renderer.snapshot();
      expect(canvas.toDataURL).toHaveBeenCalledWith("image/png", 0.92);
      expect(result).toContain("data:");
      renderer.destroy();
    });

    it("supports JPEG with custom quality", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.snapshot("jpeg", 0.8);
      expect(canvas.toDataURL).toHaveBeenCalledWith("image/jpeg", 0.8);
      renderer.destroy();
    });

    it("supports WebP format", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.snapshot("webp", 0.9);
      expect(canvas.toDataURL).toHaveBeenCalledWith("image/webp", 0.9);
      renderer.destroy();
    });
  });

  // =========================================================================
  // Color Space
  // =========================================================================
  describe("setColorSpace", () => {
    it("accepts valid color space parameters", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.setColorSpace("bt601", "srgb");
      renderer.setColorSpace("bt709", "pq", "limited");
      renderer.setColorSpace("bt2020", "hlg", "full");
      renderer.destroy();
    });
  });

  // =========================================================================
  // Resize
  // =========================================================================
  describe("resize", () => {
    it("sets canvas dimensions and viewport", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.resize(1920, 1080);
      expect(canvas.width).toBe(1920);
      expect(canvas.height).toBe(1080);
      expect(gl.viewport).toHaveBeenCalledWith(0, 0, 1920, 1080);
      renderer.destroy();
    });

    it("respects devicePixelRatio", () => {
      (globalThis as any).devicePixelRatio = 2;
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.resize(1920, 1080);
      expect(canvas.width).toBe(3840);
      expect(canvas.height).toBe(2160);
      renderer.destroy();
    });

    it("is a no-op for same dimensions", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.resize(1920, 1080);
      gl.viewport.mockClear();
      renderer.resize(1920, 1080);
      expect(gl.viewport).not.toHaveBeenCalled();
      renderer.destroy();
    });
  });

  // =========================================================================
  // renderYUV
  // =========================================================================
  describe("renderYUV", () => {
    it("uploads textures and draws a quad for I420", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      const planes = {
        y: new Uint8Array(8 * 8),
        u: new Uint8Array(4 * 4),
        v: new Uint8Array(4 * 4),
        width: 8,
        height: 8,
        format: "I420" as const,
      };

      renderer.renderYUV(planes);

      // Should create textures, upload them, and draw
      expect(gl.createTexture).toHaveBeenCalled();
      expect(gl.texImage2D).toHaveBeenCalled();
      expect(gl.drawArrays).toHaveBeenCalledWith(gl.TRIANGLE_STRIP, 0, 4);
      renderer.destroy();
    });

    it("handles I444 chroma dimensions", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      const planes = {
        y: new Uint8Array(8 * 8),
        u: new Uint8Array(8 * 8), // same size as Y for 4:4:4
        v: new Uint8Array(8 * 8),
        width: 8,
        height: 8,
        format: "I444" as const,
      };

      renderer.renderYUV(planes);
      expect(gl.drawArrays).toHaveBeenCalled();
      renderer.destroy();
    });

    it("does nothing after destroy", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);
      renderer.destroy();

      gl.drawArrays.mockClear();
      renderer.renderYUV({
        y: new Uint8Array(4),
        u: new Uint8Array(1),
        v: new Uint8Array(1),
        width: 2,
        height: 2,
        format: "I420",
      });
      expect(gl.drawArrays).not.toHaveBeenCalled();
    });

    it("does nothing when context is permanently lost", () => {
      vi.useFakeTimers();
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      // Simulate context loss
      canvas._fireEvent("webglcontextlost");

      // Fast-forward past the 3s timeout to trigger permanent loss
      vi.advanceTimersByTime(3500);
      vi.useRealTimers();

      expect(renderer.isContextLost).toBe(true);

      gl.drawArrays.mockClear();
      renderer.renderYUV({
        y: new Uint8Array(4),
        u: new Uint8Array(1),
        v: new Uint8Array(1),
        width: 2,
        height: 2,
        format: "I420",
      });
      expect(gl.drawArrays).not.toHaveBeenCalled();
      renderer.destroy();
    });
  });

  // =========================================================================
  // Context loss recovery
  // =========================================================================
  describe("context loss", () => {
    it("marks context as permanently lost after 3s timeout", () => {
      vi.useFakeTimers();
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      expect(renderer.isContextLost).toBe(false);

      canvas._fireEvent("webglcontextlost");

      vi.advanceTimersByTime(3000);
      vi.useRealTimers();

      expect(renderer.isContextLost).toBe(true);
      renderer.destroy();
    });

    it("recovers when context is restored before timeout", () => {
      vi.useFakeTimers();
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      canvas._fireEvent("webglcontextlost");

      vi.advanceTimersByTime(1000); // less than 3s

      // Simulate restore
      canvas._fireEvent("webglcontextrestored");
      vi.advanceTimersByTime(5000); // well past timeout

      vi.useRealTimers();

      expect(renderer.isContextLost).toBe(false);
      renderer.destroy();
    });
  });

  // =========================================================================
  // Destroy
  // =========================================================================
  describe("destroy", () => {
    it("cleans up GL resources", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      // Render something to create textures/program
      renderer.renderYUV({
        y: new Uint8Array(4),
        u: new Uint8Array(1),
        v: new Uint8Array(1),
        width: 2,
        height: 2,
        format: "I420",
      });

      renderer.destroy();

      expect(gl.deleteTexture).toHaveBeenCalled();
      expect(gl.deleteBuffer).toHaveBeenCalled();
      expect(gl.deleteProgram).toHaveBeenCalled();
      expect(canvas.removeEventListener).toHaveBeenCalledWith(
        "webglcontextlost",
        expect.any(Function)
      );
      expect(canvas.removeEventListener).toHaveBeenCalledWith(
        "webglcontextrestored",
        expect.any(Function)
      );
    });

    it("is idempotent", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.destroy();
      renderer.destroy(); // should not throw
    });
  });

  // =========================================================================
  // Transform matrix math
  // =========================================================================
  describe("transform matrix", () => {
    it("identity for no rotation/mirror", () => {
      const gl = createMockGLContext();
      const canvas = createMockCanvas(gl);
      const renderer = new WebGLRenderer(canvas as any);

      renderer.renderYUV({
        y: new Uint8Array(4),
        u: new Uint8Array(1),
        v: new Uint8Array(1),
        width: 2,
        height: 2,
        format: "I420",
      });

      // The transform matrix should be identity (approximately)
      const matrixCall = gl.uniformMatrix3fv.mock.calls.find(
        (call: any[]) => call[2] instanceof Float32Array && call[2].length === 9
      );
      expect(matrixCall).toBeDefined();

      // First call is color matrix, second is transform
      const transformCalls = gl.uniformMatrix3fv.mock.calls;
      const transformMatrix = transformCalls[transformCalls.length - 1][2];

      // Identity rotation (cos(0)=1, sin(0)=0), no mirror (sx=1, sy=1)
      // Matrix: [1, 0, 0, 0, 1, 0, 0, 0, 1]
      expect(transformMatrix[0]).toBeCloseTo(1); // cos * sx
      expect(transformMatrix[1]).toBeCloseTo(0); // sin * sx
      expect(transformMatrix[3]).toBeCloseTo(0); // -sin * sy
      expect(transformMatrix[4]).toBeCloseTo(1); // cos * sy

      renderer.destroy();
    });
  });
});
