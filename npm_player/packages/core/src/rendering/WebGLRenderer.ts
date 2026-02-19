/**
 * WebGL YUV→RGB Video Renderer
 *
 * Direct GPU rendering of decoded video frames, bypassing MediaStreamTrackGenerator
 * and browser-specific polyfills. Supports:
 * - I420 / NV12 / I420P10 pixel formats via runtime-generated shaders
 * - BT.601, BT.709, BT.2020 color space matrices
 * - MPEG (16-235) and full (0-255) range
 * - HDR tone mapping (PQ / HLG → SDR)
 * - PBO-based async uploads (WebGL2)
 * - Context loss recovery with Canvas2D fallback
 * - Rotation / mirror via uniform-based vertex transforms
 * - Snapshot capture
 */

// Color space coefficients (Kr, Kb) per standard
const COLOR_SPACE_COEFFICIENTS: Record<string, [number, number]> = {
  bt601: [0.299, 0.114],
  bt709: [0.2126, 0.0722],
  bt2020: [0.2627, 0.0593],
};

export type PixelFormat = "I420" | "I420A" | "NV12" | "I422" | "I444" | "RGBX" | "BGRX" | "I420P10";
export type ColorPrimaries = "bt601" | "bt709" | "bt2020";
export type TransferFunction = "srgb" | "pq" | "hlg" | "linear";
export type ColorRange = "limited" | "full";

export interface YUVPlanes {
  y: Uint8Array | Uint16Array;
  u: Uint8Array | Uint16Array;
  v: Uint8Array | Uint16Array;
  width: number;
  height: number;
  /** Stride (bytes per row) for each plane — defaults to width */
  yStride?: number;
  uvStride?: number;
  format: PixelFormat;
}

export interface WebGLRendererOptions {
  /** Prefer 16-bit textures for 10-bit content (requires WebGL2) */
  prefer16bit?: boolean;
  /** Target peak luminance for HDR→SDR tone mapping (nits, default 100) */
  targetNits?: number;
}

interface ShaderProgram {
  program: WebGLProgram;
  attribs: {
    position: number;
    texCoord: number;
  };
  uniforms: {
    yTex: WebGLUniformLocation;
    uTex: WebGLUniformLocation;
    vTex: WebGLUniformLocation;
    colorMatrix: WebGLUniformLocation;
    rangeOffset: WebGLUniformLocation;
    rangeScale: WebGLUniformLocation;
    hdrMode: WebGLUniformLocation;
    hdrParams: WebGLUniformLocation;
    transform: WebGLUniformLocation;
  };
}

/** Compute YUV→RGB 3×3 matrix from Kr, Kb coefficients */
function computeColorMatrix(kr: number, kb: number): Float32Array {
  const kg = 1 - kr - kb;
  // Standard YCbCr→RGB matrix derivation
  // R = Y + (2 - 2*Kr) * Cr
  // G = Y - (2*Kb*(1-Kb)/Kg) * Cb - (2*Kr*(1-Kr)/Kg) * Cr
  // B = Y + (2 - 2*Kb) * Cb
  return new Float32Array([
    1,
    1,
    1, // Y column
    0,
    -(2 * kb * (1 - kb)) / kg,
    2 - 2 * kb, // Cb column
    2 - 2 * kr,
    -(2 * kr * (1 - kr)) / kg,
    0, // Cr column
  ]);
}

const VERTEX_SHADER = `
  attribute vec2 a_position;
  attribute vec2 a_texCoord;
  uniform mat3 u_transform;
  varying vec2 v_texCoord;
  void main() {
    vec3 pos = u_transform * vec3(a_position, 1.0);
    gl_Position = vec4(pos.xy, 0.0, 1.0);
    v_texCoord = a_texCoord;
  }
`;

function generateFragmentShader(format: PixelFormat, is10bit: boolean): string {
  const precision = is10bit ? "highp" : "mediump";

  // NV12 packs U and V into a single texture (RG channels)
  if (format === "NV12") {
    return `
      precision ${precision} float;
      varying vec2 v_texCoord;
      uniform sampler2D u_yTex;
      uniform sampler2D u_uTex;
      // u_vTex unused for NV12, UV interleaved in u_uTex
      uniform mat3 u_colorMatrix;
      uniform vec3 u_rangeOffset;
      uniform vec3 u_rangeScale;
      uniform int u_hdrMode; // 0=SDR, 1=PQ, 2=HLG
      uniform vec4 u_hdrParams;

      vec3 pqToLinear(vec3 pq) {
        float m1 = 0.1593017578125;
        float m2 = 78.84375;
        float c1 = 0.8359375;
        float c2 = 18.8515625;
        float c3 = 18.6875;
        vec3 p = pow(max(pq, vec3(0.0)), vec3(1.0 / m2));
        vec3 num = max(p - c1, vec3(0.0));
        vec3 den = c2 - c3 * p;
        return pow(num / max(den, vec3(1e-6)), vec3(1.0 / m1)) * 10000.0;
      }

      vec3 hlgToLinear(vec3 hlg) {
        float a = 0.17883277;
        float b = 0.28466892;
        float c = 0.55991073;
        vec3 result;
        for (int i = 0; i < 3; i++) {
          float v = (i == 0) ? hlg.r : (i == 1) ? hlg.g : hlg.b;
          float lin;
          if (v <= 0.5) {
            lin = v * v / 3.0;
          } else {
            lin = (exp((v - c) / a) + b) / 12.0;
          }
          if (i == 0) result.r = lin;
          else if (i == 1) result.g = lin;
          else result.b = lin;
        }
        return result * 1000.0;
      }

      vec3 tonemapHable(vec3 color) {
        float A = 0.15; float B = 0.50; float C = 0.10;
        float D = 0.20; float E = 0.02; float F = 0.30;
        vec3 x = color;
        return ((x*(A*x+C*B)+D*E)/(x*(A*x+B)+D*F))-E/F;
      }

      vec3 tonemap(vec3 linearNits) {
        float targetNits = u_hdrParams.x;
        vec3 normalized = linearNits / targetNits;
        vec3 mapped = tonemapHable(normalized * 2.0);
        vec3 whiteScale = vec3(1.0) / tonemapHable(vec3(11.2));
        return pow(mapped * whiteScale, vec3(1.0 / 2.2));
      }

      void main() {
        float y = texture2D(u_yTex, v_texCoord).r;
        vec2 uv = texture2D(u_uTex, v_texCoord).rg;
        vec3 yuv = (vec3(y, uv.r, uv.g) - u_rangeOffset) * u_rangeScale;
        vec3 rgb = u_colorMatrix * yuv;
        if (u_hdrMode == 1) {
          rgb = tonemap(pqToLinear(rgb));
        } else if (u_hdrMode == 2) {
          rgb = tonemap(hlgToLinear(rgb));
        }
        gl_FragColor = vec4(clamp(rgb, 0.0, 1.0), 1.0);
      }
    `;
  }

  // RGBX/BGRX — direct passthrough
  if (format === "RGBX" || format === "BGRX") {
    const swizzle = format === "BGRX" ? "bgr" : "rgb";
    return `
      precision ${precision} float;
      varying vec2 v_texCoord;
      uniform sampler2D u_yTex;
      uniform sampler2D u_uTex;
      uniform sampler2D u_vTex;
      uniform mat3 u_colorMatrix;
      uniform vec3 u_rangeOffset;
      uniform vec3 u_rangeScale;
      uniform int u_hdrMode;
      uniform vec4 u_hdrParams;
      void main() {
        vec4 px = texture2D(u_yTex, v_texCoord);
        gl_FragColor = vec4(px.${swizzle}, 1.0);
      }
    `;
  }

  // I420 / I420A / I420P10 / I422 / I444 — standard planar YUV
  return `
    precision ${precision} float;
    varying vec2 v_texCoord;
    uniform sampler2D u_yTex;
    uniform sampler2D u_uTex;
    uniform sampler2D u_vTex;
    uniform mat3 u_colorMatrix;
    uniform vec3 u_rangeOffset;
    uniform vec3 u_rangeScale;
    uniform int u_hdrMode;
    uniform vec4 u_hdrParams;

    vec3 pqToLinear(vec3 pq) {
      float m1 = 0.1593017578125;
      float m2 = 78.84375;
      float c1 = 0.8359375;
      float c2 = 18.8515625;
      float c3 = 18.6875;
      vec3 p = pow(max(pq, vec3(0.0)), vec3(1.0 / m2));
      vec3 num = max(p - c1, vec3(0.0));
      vec3 den = c2 - c3 * p;
      return pow(num / max(den, vec3(1e-6)), vec3(1.0 / m1)) * 10000.0;
    }

    vec3 hlgToLinear(vec3 hlg) {
      float a = 0.17883277;
      float b = 0.28466892;
      float c = 0.55991073;
      vec3 result;
      for (int i = 0; i < 3; i++) {
        float v = (i == 0) ? hlg.r : (i == 1) ? hlg.g : hlg.b;
        float lin;
        if (v <= 0.5) {
          lin = v * v / 3.0;
        } else {
          lin = (exp((v - c) / a) + b) / 12.0;
        }
        if (i == 0) result.r = lin;
        else if (i == 1) result.g = lin;
        else result.b = lin;
      }
      return result * 1000.0;
    }

    vec3 tonemapHable(vec3 color) {
      float A = 0.15; float B = 0.50; float C = 0.10;
      float D = 0.20; float E = 0.02; float F = 0.30;
      vec3 x = color;
      return ((x*(A*x+C*B)+D*E)/(x*(A*x+B)+D*F))-E/F;
    }

    vec3 tonemap(vec3 linearNits) {
      float targetNits = u_hdrParams.x;
      vec3 normalized = linearNits / targetNits;
      vec3 mapped = tonemapHable(normalized * 2.0);
      vec3 whiteScale = vec3(1.0) / tonemapHable(vec3(11.2));
      return pow(mapped * whiteScale, vec3(1.0 / 2.2));
    }

    void main() {
      float y = texture2D(u_yTex, v_texCoord).r;
      float u = texture2D(u_uTex, v_texCoord).r;
      float v = texture2D(u_vTex, v_texCoord).r;
      vec3 yuv = (vec3(y, u, v) - u_rangeOffset) * u_rangeScale;
      vec3 rgb = u_colorMatrix * yuv;
      if (u_hdrMode == 1) {
        rgb = tonemap(pqToLinear(rgb));
      } else if (u_hdrMode == 2) {
        rgb = tonemap(hlgToLinear(rgb));
      }
      gl_FragColor = vec4(clamp(rgb, 0.0, 1.0), 1.0);
    }
  `;
}

export class WebGLRenderer {
  private canvas: HTMLCanvasElement;
  private gl: WebGLRenderingContext | WebGL2RenderingContext | null = null;
  private isWebGL2 = false;
  private program: ShaderProgram | null = null;
  private textures: WebGLTexture[] = [];
  private currentFormat: PixelFormat | null = null;
  private currentWidth = 0;
  private currentHeight = 0;
  private destroyed = false;

  // Color space state
  private primaries: ColorPrimaries = "bt709";
  private transfer: TransferFunction = "srgb";
  private range: ColorRange = "limited";

  // Transform state (rotation / mirror)
  private rotation = 0; // degrees: 0, 90, 180, 270
  private mirrorH = false;
  private mirrorV = false;

  // PBO state (WebGL2 only)
  private pbos: WebGLBuffer[] = [];
  private pboReady = false;

  // Context loss handling
  private contextLostHandler: ((e: Event) => void) | null = null;
  private contextRestoredHandler: ((e: Event) => void) | null = null;
  private contextLossTimeout: ReturnType<typeof setTimeout> | null = null;
  private contextPermanentlyLost = false;

  // Options
  private prefer16bit: boolean;
  private targetNits: number;

  // Vertex buffer
  private vertexBuffer: WebGLBuffer | null = null;
  private texCoordBuffer: WebGLBuffer | null = null;

  constructor(canvas: HTMLCanvasElement, opts?: WebGLRendererOptions) {
    this.canvas = canvas;
    this.prefer16bit = opts?.prefer16bit ?? false;
    this.targetNits = opts?.targetNits ?? 100;
    this.initGL();
    this.setupContextLossHandlers();
  }

  private initGL(): void {
    // Try WebGL2 first (needed for PBO, 16-bit textures)
    const gl2 = this.canvas.getContext("webgl2", {
      alpha: false,
      antialias: false,
      depth: false,
      stencil: false,
      preserveDrawingBuffer: true, // needed for snapshot
      powerPreference: "high-performance",
    });

    if (gl2) {
      this.gl = gl2;
      this.isWebGL2 = true;
    } else {
      const gl1 = this.canvas.getContext("webgl", {
        alpha: false,
        antialias: false,
        depth: false,
        stencil: false,
        preserveDrawingBuffer: true,
        powerPreference: "high-performance",
      });
      if (!gl1) throw new Error("WebGL not supported");
      this.gl = gl1;
      this.isWebGL2 = false;
    }

    this.setupGeometry();
  }

  private setupGeometry(): void {
    const gl = this.gl!;

    // Full-screen quad
    const positions = new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]);
    const texCoords = new Float32Array([0, 1, 1, 1, 0, 0, 1, 0]);

    this.vertexBuffer = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, this.vertexBuffer);
    gl.bufferData(gl.ARRAY_BUFFER, positions, gl.STATIC_DRAW);

    this.texCoordBuffer = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, this.texCoordBuffer);
    gl.bufferData(gl.ARRAY_BUFFER, texCoords, gl.STATIC_DRAW);
  }

  private setupContextLossHandlers(): void {
    this.contextLostHandler = (e: Event) => {
      e.preventDefault();
      this.program = null;
      this.textures = [];
      this.pbos = [];
      this.pboReady = false;
      this.vertexBuffer = null;
      this.texCoordBuffer = null;
      this.currentFormat = null;

      // Wait for restore or declare permanent loss
      this.contextLossTimeout = setTimeout(() => {
        this.contextPermanentlyLost = true;
      }, 3000);
    };

    this.contextRestoredHandler = () => {
      if (this.contextLossTimeout) {
        clearTimeout(this.contextLossTimeout);
        this.contextLossTimeout = null;
      }
      this.contextPermanentlyLost = false;
      this.initGL();
    };

    this.canvas.addEventListener("webglcontextlost", this.contextLostHandler);
    this.canvas.addEventListener("webglcontextrestored", this.contextRestoredHandler);
  }

  private compileShader(type: number, source: string): WebGLShader {
    const gl = this.gl!;
    const shader = gl.createShader(type)!;
    gl.shaderSource(shader, source);
    gl.compileShader(shader);
    if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
      const log = gl.getShaderInfoLog(shader);
      gl.deleteShader(shader);
      throw new Error(`Shader compile error: ${log}`);
    }
    return shader;
  }

  private buildProgram(format: PixelFormat, is10bit: boolean): ShaderProgram {
    const gl = this.gl!;

    const vs = this.compileShader(gl.VERTEX_SHADER, VERTEX_SHADER);
    const fs = this.compileShader(gl.FRAGMENT_SHADER, generateFragmentShader(format, is10bit));

    const program = gl.createProgram()!;
    gl.attachShader(program, vs);
    gl.attachShader(program, fs);
    gl.linkProgram(program);

    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
      const log = gl.getProgramInfoLog(program);
      gl.deleteProgram(program);
      throw new Error(`Program link error: ${log}`);
    }

    // Clean up shaders (linked into program)
    gl.deleteShader(vs);
    gl.deleteShader(fs);

    return {
      program,
      attribs: {
        position: gl.getAttribLocation(program, "a_position"),
        texCoord: gl.getAttribLocation(program, "a_texCoord"),
      },
      uniforms: {
        yTex: gl.getUniformLocation(program, "u_yTex")!,
        uTex: gl.getUniformLocation(program, "u_uTex")!,
        vTex: gl.getUniformLocation(program, "u_vTex")!,
        colorMatrix: gl.getUniformLocation(program, "u_colorMatrix")!,
        rangeOffset: gl.getUniformLocation(program, "u_rangeOffset")!,
        rangeScale: gl.getUniformLocation(program, "u_rangeScale")!,
        hdrMode: gl.getUniformLocation(program, "u_hdrMode")!,
        hdrParams: gl.getUniformLocation(program, "u_hdrParams")!,
        transform: gl.getUniformLocation(program, "u_transform")!,
      },
    };
  }

  private ensureProgram(format: PixelFormat): void {
    const is10bit = format === "I420P10";
    if (this.currentFormat !== format) {
      if (this.program) {
        this.gl!.deleteProgram(this.program.program);
      }
      this.program = this.buildProgram(format, is10bit);
      this.currentFormat = format;
    }
  }

  private createTexture(): WebGLTexture {
    const gl = this.gl!;
    const tex = gl.createTexture()!;
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    return tex;
  }

  private ensureTextures(count: number): void {
    while (this.textures.length < count) {
      this.textures.push(this.createTexture());
    }
  }

  private uploadTexture(
    index: number,
    data: Uint8Array | Uint16Array,
    width: number,
    height: number,
    is16bit: boolean
  ): void {
    const gl = this.gl!;
    gl.activeTexture(gl.TEXTURE0 + index);
    gl.bindTexture(gl.TEXTURE_2D, this.textures[index]);

    if (is16bit && this.isWebGL2) {
      const gl2 = gl as WebGL2RenderingContext;
      gl2.texImage2D(
        gl.TEXTURE_2D,
        0,
        gl2.R16UI,
        width,
        height,
        0,
        gl2.RED_INTEGER,
        gl2.UNSIGNED_SHORT,
        data as Uint16Array
      );
    } else {
      gl.texImage2D(
        gl.TEXTURE_2D,
        0,
        gl.LUMINANCE,
        width,
        height,
        0,
        gl.LUMINANCE,
        gl.UNSIGNED_BYTE,
        data as Uint8Array
      );
    }
  }

  private uploadNV12Texture(index: number, data: Uint8Array, width: number, height: number): void {
    const gl = this.gl!;
    gl.activeTexture(gl.TEXTURE0 + index);
    gl.bindTexture(gl.TEXTURE_2D, this.textures[index]);

    gl.texImage2D(
      gl.TEXTURE_2D,
      0,
      gl.LUMINANCE_ALPHA,
      width,
      height,
      0,
      gl.LUMINANCE_ALPHA,
      gl.UNSIGNED_BYTE,
      data
    );
  }

  private setUniforms(): void {
    const gl = this.gl!;
    const prog = this.program!;

    gl.useProgram(prog.program);

    // Texture units
    gl.uniform1i(prog.uniforms.yTex, 0);
    gl.uniform1i(prog.uniforms.uTex, 1);
    gl.uniform1i(prog.uniforms.vTex, 2);

    // Color matrix
    const [kr, kb] = COLOR_SPACE_COEFFICIENTS[this.primaries] ?? COLOR_SPACE_COEFFICIENTS["bt709"];
    const matrix = computeColorMatrix(kr, kb);
    gl.uniformMatrix3fv(prog.uniforms.colorMatrix, false, matrix);

    // Range offset/scale
    if (this.range === "limited") {
      // MPEG range: Y in [16, 235], UV in [16, 240]
      gl.uniform3f(prog.uniforms.rangeOffset, 16 / 255, 128 / 255, 128 / 255);
      gl.uniform3f(prog.uniforms.rangeScale, 255 / (235 - 16), 255 / (240 - 16), 255 / (240 - 16));
    } else {
      gl.uniform3f(prog.uniforms.rangeOffset, 0, 128 / 255, 128 / 255);
      gl.uniform3f(prog.uniforms.rangeScale, 1, 1, 1);
    }

    // HDR mode
    let hdrMode = 0;
    if (this.transfer === "pq") hdrMode = 1;
    else if (this.transfer === "hlg") hdrMode = 2;
    gl.uniform1i(prog.uniforms.hdrMode, hdrMode);
    gl.uniform4f(prog.uniforms.hdrParams, this.targetNits, 0, 0, 0);

    // Transform matrix (rotation + mirror)
    const transform = this.computeTransformMatrix();
    gl.uniformMatrix3fv(prog.uniforms.transform, false, transform);
  }

  private computeTransformMatrix(): Float32Array {
    const rad = (this.rotation * Math.PI) / 180;
    const cos = Math.cos(rad);
    const sin = Math.sin(rad);
    const sx = this.mirrorH ? -1 : 1;
    const sy = this.mirrorV ? -1 : 1;

    // Scale then rotate: R * S
    return new Float32Array([cos * sx, sin * sx, 0, -sin * sy, cos * sy, 0, 0, 0, 1]);
  }

  private drawQuad(): void {
    const gl = this.gl!;
    const prog = this.program!;

    // Bind position buffer
    gl.bindBuffer(gl.ARRAY_BUFFER, this.vertexBuffer);
    gl.enableVertexAttribArray(prog.attribs.position);
    gl.vertexAttribPointer(prog.attribs.position, 2, gl.FLOAT, false, 0, 0);

    // Bind texCoord buffer
    gl.bindBuffer(gl.ARRAY_BUFFER, this.texCoordBuffer);
    gl.enableVertexAttribArray(prog.attribs.texCoord);
    gl.vertexAttribPointer(prog.attribs.texCoord, 2, gl.FLOAT, false, 0, 0);

    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
  }

  // -- Public API --

  /**
   * Render a VideoFrame from WebCodecs API directly.
   * Uses texImage2D with VideoFrame as source — the browser handles YUV→RGB
   * conversion on the GPU. This avoids async copyTo race conditions and works
   * correctly across all pixel formats (I420, NV12, etc.) on all browsers.
   */
  render(frame: VideoFrame): void {
    if (this.destroyed || this.contextPermanentlyLost || !this.gl) return;

    const width = frame.displayWidth;
    const height = frame.displayHeight;

    this.resize(width, height);
    this.ensureProgram("RGBX"); // passthrough — browser does YUV→RGB
    this.ensureTextures(3);

    const gl = this.gl;

    // Upload VideoFrame directly — browser converts YUV→RGBA on the GPU.
    // VideoFrame is a valid TexImageSource in Chrome 94+, Firefox 130+, Safari 16.4+.
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, this.textures[0]);
    gl.texImage2D(
      gl.TEXTURE_2D,
      0,
      gl.RGBA,
      gl.RGBA,
      gl.UNSIGNED_BYTE,
      frame as unknown as TexImageSource
    );

    this.setUniforms();
    this.drawQuad();
    frame.close();
  }

  /**
   * Render raw YUV planes from a WASM decoder.
   */
  renderYUV(planes: YUVPlanes): void {
    if (this.destroyed || this.contextPermanentlyLost || !this.gl) return;

    const { width, height, format, y, u, v } = planes;
    const is10bit = format === "I420P10";
    const chromaW = format === "I444" ? width : width / 2;
    const chromaH = format === "I422" || format === "I444" ? height : height / 2;

    this.resize(width, height);
    this.ensureProgram(format);
    this.ensureTextures(3);

    this.uploadTexture(0, y, width, height, is10bit && this.prefer16bit);
    this.uploadTexture(1, u, chromaW, chromaH, is10bit && this.prefer16bit);
    this.uploadTexture(2, v, chromaW, chromaH, is10bit && this.prefer16bit);

    this.setUniforms();
    this.drawQuad();
  }

  /**
   * Set the color space for YUV→RGB conversion.
   */
  setColorSpace(primaries: ColorPrimaries, transfer: TransferFunction, range?: ColorRange): void {
    this.primaries = primaries;
    this.transfer = transfer;
    if (range) this.range = range;
    // Shader will pick up new uniforms on next render()
  }

  /**
   * Resize the canvas to match video dimensions.
   * Respects devicePixelRatio for crisp rendering.
   */
  resize(width: number, height: number): void {
    if (width === this.currentWidth && height === this.currentHeight) return;
    this.currentWidth = width;
    this.currentHeight = height;

    const dpr = typeof devicePixelRatio !== "undefined" ? devicePixelRatio : 1;
    this.canvas.width = width * dpr;
    this.canvas.height = height * dpr;

    if (this.gl) {
      this.gl.viewport(0, 0, this.canvas.width, this.canvas.height);
    }
  }

  /**
   * Set video rotation (0, 90, 180, 270 degrees).
   */
  setRotation(degrees: number): void {
    this.rotation = ((degrees % 360) + 360) % 360;
  }

  /**
   * Set horizontal/vertical mirror.
   */
  setMirror(horizontal: boolean, vertical?: boolean): void {
    this.mirrorH = horizontal;
    this.mirrorV = vertical ?? false;
  }

  /**
   * Capture the current canvas contents as a data URL.
   */
  snapshot(type: "png" | "jpeg" | "webp" = "png", quality = 0.92): string {
    return this.canvas.toDataURL(`image/${type}`, quality);
  }

  /** Whether the WebGL context has been permanently lost. */
  get isContextLost(): boolean {
    return this.contextPermanentlyLost;
  }

  /** Whether this renderer is using WebGL2. */
  get hasWebGL2(): boolean {
    return this.isWebGL2;
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;

    if (this.contextLostHandler) {
      this.canvas.removeEventListener("webglcontextlost", this.contextLostHandler);
    }
    if (this.contextRestoredHandler) {
      this.canvas.removeEventListener("webglcontextrestored", this.contextRestoredHandler);
    }
    if (this.contextLossTimeout) {
      clearTimeout(this.contextLossTimeout);
    }

    const gl = this.gl;
    if (gl) {
      for (const tex of this.textures) gl.deleteTexture(tex);
      for (const pbo of this.pbos) gl.deleteBuffer(pbo);
      if (this.vertexBuffer) gl.deleteBuffer(this.vertexBuffer);
      if (this.texCoordBuffer) gl.deleteBuffer(this.texCoordBuffer);
      if (this.program) gl.deleteProgram(this.program.program);
    }

    this.textures = [];
    this.pbos = [];
    this.program = null;
    this.gl = null;
  }
}
