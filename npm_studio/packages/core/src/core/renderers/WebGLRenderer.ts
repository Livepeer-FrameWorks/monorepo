/**
 * WebGL2 Renderer
 *
 * High-performance renderer using raw WebGL2 for GPU-accelerated compositing.
 * Replaces PixiJS for better Web Worker and OffscreenCanvas support.
 *
 * Features:
 * - Layer compositing with z-order
 * - Transform: position, size, rotation, opacity
 * - Border radius via fragment shader SDF
 * - Cropping
 * - Transitions: fade, slide (left/right/up/down), cut
 * - Real-time filters: blur, color matrix, grayscale, sepia
 *
 * Requirements:
 * - WebGL2 support (universal in modern browsers)
 * - OffscreenCanvas support for worker context
 */

import type {
  Scene,
  Layer,
  CompositorConfig,
  TransitionType,
  FilterConfig,
  RendererType,
  RendererStats,
} from '../../types';
import { registerRenderer, type CompositorRenderer } from './index';

// ============================================================================
// Shader Sources
// ============================================================================

const VERTEX_SHADER = `#version 300 es
precision highp float;

// Quad vertices (2 triangles = 6 vertices)
const vec2 VERTICES[6] = vec2[6](
  vec2(0.0, 0.0),
  vec2(1.0, 0.0),
  vec2(0.0, 1.0),
  vec2(1.0, 0.0),
  vec2(1.0, 1.0),
  vec2(0.0, 1.0)
);

// Uniforms for layer transform
uniform vec2 u_resolution;      // Canvas size in pixels
uniform vec2 u_position;        // Layer position (0-1 relative)
uniform vec2 u_size;            // Layer size (0-1 relative)
uniform float u_rotation;       // Rotation in radians
uniform vec4 u_crop;            // Crop (left, top, right, bottom)

out vec2 v_texCoord;
out vec2 v_localPos;            // Position within layer (0-1)
out vec2 v_layerSize;           // Layer size in pixels

void main() {
  vec2 vertex = VERTICES[gl_VertexID];

  // Apply texture coordinate cropping
  float cropLeft = u_crop.x;
  float cropTop = u_crop.y;
  float cropRight = u_crop.z;
  float cropBottom = u_crop.w;

  v_texCoord = vec2(
    cropLeft + vertex.x * (1.0 - cropLeft - cropRight),
    cropTop + vertex.y * (1.0 - cropTop - cropBottom)
  );

  // Store local position for border radius calculation
  v_localPos = vertex;
  v_layerSize = u_size * u_resolution;

  // Calculate layer position in pixels
  vec2 layerPos = u_position * u_resolution;
  vec2 layerSize = u_size * u_resolution;

  // Transform vertex position
  vec2 pos = layerPos + vertex * layerSize;

  // Apply rotation around layer center
  if (u_rotation != 0.0) {
    vec2 center = layerPos + layerSize * 0.5;
    pos -= center;
    float c = cos(u_rotation);
    float s = sin(u_rotation);
    pos = vec2(pos.x * c - pos.y * s, pos.x * s + pos.y * c);
    pos += center;
  }

  // Convert to clip space (-1 to 1)
  vec2 clipPos = (pos / u_resolution) * 2.0 - 1.0;

  // Flip Y for WebGL coordinate system
  clipPos.y = -clipPos.y;

  gl_Position = vec4(clipPos, 0.0, 1.0);
}
`;

const FRAGMENT_SHADER = `#version 300 es
precision highp float;

uniform sampler2D u_texture;
uniform float u_opacity;
uniform float u_borderRadius;   // Border radius in pixels

// Filter uniforms
uniform int u_filterType;       // 0=none, 1=blur, 2=colorMatrix, 3=grayscale, 4=sepia
uniform float u_filterStrength;
uniform mat4 u_colorMatrix;

in vec2 v_texCoord;
in vec2 v_localPos;
in vec2 v_layerSize;

out vec4 fragColor;

// Signed distance function for rounded rectangle
float roundedRectSDF(vec2 pos, vec2 size, float radius) {
  vec2 q = abs(pos - 0.5) * size - size * 0.5 + radius;
  return min(max(q.x, q.y), 0.0) + length(max(q, 0.0)) - radius;
}

void main() {
  vec4 color = texture(u_texture, v_texCoord);

  // Apply filter effects
  if (u_filterType == 2) {
    // Color matrix
    color = u_colorMatrix * color;
  } else if (u_filterType == 3) {
    // Grayscale
    float gray = dot(color.rgb, vec3(0.299, 0.587, 0.114));
    color.rgb = mix(color.rgb, vec3(gray), u_filterStrength);
  } else if (u_filterType == 4) {
    // Sepia
    vec3 sepia = vec3(
      dot(color.rgb, vec3(0.393, 0.769, 0.189)),
      dot(color.rgb, vec3(0.349, 0.686, 0.168)),
      dot(color.rgb, vec3(0.272, 0.534, 0.131))
    );
    color.rgb = mix(color.rgb, sepia, u_filterStrength);
  }

  // Apply border radius clipping
  if (u_borderRadius > 0.0) {
    float dist = roundedRectSDF(v_localPos, v_layerSize, u_borderRadius);
    // Smooth anti-aliased edge
    float alpha = 1.0 - smoothstep(-1.0, 1.0, dist);
    color.a *= alpha;
  }

  // Apply opacity
  color.a *= u_opacity;

  fragColor = color;
}
`;

// Simpler shader for transitions (full-screen quads)
const TRANSITION_VERTEX_SHADER = `#version 300 es
precision highp float;

const vec2 VERTICES[6] = vec2[6](
  vec2(-1.0, -1.0),
  vec2(1.0, -1.0),
  vec2(-1.0, 1.0),
  vec2(1.0, -1.0),
  vec2(1.0, 1.0),
  vec2(-1.0, 1.0)
);

uniform vec2 u_offset;          // Slide offset

out vec2 v_texCoord;

void main() {
  vec2 vertex = VERTICES[gl_VertexID];
  v_texCoord = (vertex + 1.0) * 0.5;
  v_texCoord.y = 1.0 - v_texCoord.y; // Flip Y
  gl_Position = vec4(vertex + u_offset * 2.0, 0.0, 1.0);
}
`;

const TRANSITION_FRAGMENT_SHADER = `#version 300 es
precision highp float;

uniform sampler2D u_texture;
uniform float u_opacity;

in vec2 v_texCoord;
out vec4 fragColor;

void main() {
  vec4 color = texture(u_texture, v_texCoord);
  color.a *= u_opacity;
  fragColor = color;
}
`;

// ============================================================================
// WebGL2 Renderer Implementation
// ============================================================================

function checkWebGLSupport(): boolean {
  try {
    return typeof WebGL2RenderingContext !== 'undefined';
  } catch {
    return false;
  }
}

export class WebGLRenderer implements CompositorRenderer {
  readonly type: RendererType = 'webgl';
  readonly isSupported = checkWebGLSupport();

  private gl!: WebGL2RenderingContext;
  private canvas!: OffscreenCanvas;
  private config!: CompositorConfig;

  // Shader programs
  private layerProgram!: WebGLProgram;
  private transitionProgram!: WebGLProgram;

  // Uniform locations for layer program
  private layerUniforms!: {
    resolution: WebGLUniformLocation;
    position: WebGLUniformLocation;
    size: WebGLUniformLocation;
    rotation: WebGLUniformLocation;
    crop: WebGLUniformLocation;
    texture: WebGLUniformLocation;
    opacity: WebGLUniformLocation;
    borderRadius: WebGLUniformLocation;
    filterType: WebGLUniformLocation;
    filterStrength: WebGLUniformLocation;
    colorMatrix: WebGLUniformLocation;
  };

  // Uniform locations for transition program
  private transitionUniforms!: {
    texture: WebGLUniformLocation;
    opacity: WebGLUniformLocation;
    offset: WebGLUniformLocation;
  };

  // Texture management
  private textures: Map<string, WebGLTexture> = new Map();
  private textureCache: Map<string, { width: number; height: number }> = new Map();

  // Filter configurations
  private filterConfigs: Map<string, FilterConfig> = new Map();

  // Stats tracking
  private frameCount = 0;
  private lastFrameTime = 0;
  private fps = 0;
  private lastRenderTime = 0;

  async init(canvas: OffscreenCanvas, config: CompositorConfig): Promise<void> {
    if (!this.isSupported) {
      throw new Error('WebGL2 is not supported in this environment');
    }

    this.canvas = canvas;
    this.config = config;

    // Create WebGL2 context
    const gl = canvas.getContext('webgl2', {
      alpha: false,
      antialias: false,
      depth: false,
      stencil: false,
      premultipliedAlpha: true,
      preserveDrawingBuffer: false,
      powerPreference: 'high-performance',
      desynchronized: true, // Lower latency
    });

    if (!gl) {
      throw new Error('Failed to create WebGL2 context');
    }

    this.gl = gl;

    // Enable blending for transparency
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);

    // Compile shaders and create programs
    this.layerProgram = this.createProgram(VERTEX_SHADER, FRAGMENT_SHADER);
    this.transitionProgram = this.createProgram(TRANSITION_VERTEX_SHADER, TRANSITION_FRAGMENT_SHADER);

    // Get uniform locations for layer program
    this.layerUniforms = {
      resolution: gl.getUniformLocation(this.layerProgram, 'u_resolution')!,
      position: gl.getUniformLocation(this.layerProgram, 'u_position')!,
      size: gl.getUniformLocation(this.layerProgram, 'u_size')!,
      rotation: gl.getUniformLocation(this.layerProgram, 'u_rotation')!,
      crop: gl.getUniformLocation(this.layerProgram, 'u_crop')!,
      texture: gl.getUniformLocation(this.layerProgram, 'u_texture')!,
      opacity: gl.getUniformLocation(this.layerProgram, 'u_opacity')!,
      borderRadius: gl.getUniformLocation(this.layerProgram, 'u_borderRadius')!,
      filterType: gl.getUniformLocation(this.layerProgram, 'u_filterType')!,
      filterStrength: gl.getUniformLocation(this.layerProgram, 'u_filterStrength')!,
      colorMatrix: gl.getUniformLocation(this.layerProgram, 'u_colorMatrix')!,
    };

    // Get uniform locations for transition program
    this.transitionUniforms = {
      texture: gl.getUniformLocation(this.transitionProgram, 'u_texture')!,
      opacity: gl.getUniformLocation(this.transitionProgram, 'u_opacity')!,
      offset: gl.getUniformLocation(this.transitionProgram, 'u_offset')!,
    };

    // Set viewport
    gl.viewport(0, 0, config.width, config.height);

    this.lastFrameTime = performance.now();
    console.log('[WebGLRenderer] Initialized with raw WebGL2');
  }

  private createProgram(vertexSource: string, fragmentSource: string): WebGLProgram {
    const gl = this.gl;

    // Compile vertex shader
    const vertexShader = gl.createShader(gl.VERTEX_SHADER)!;
    gl.shaderSource(vertexShader, vertexSource);
    gl.compileShader(vertexShader);

    if (!gl.getShaderParameter(vertexShader, gl.COMPILE_STATUS)) {
      const log = gl.getShaderInfoLog(vertexShader);
      gl.deleteShader(vertexShader);
      throw new Error(`Vertex shader compilation failed: ${log}`);
    }

    // Compile fragment shader
    const fragmentShader = gl.createShader(gl.FRAGMENT_SHADER)!;
    gl.shaderSource(fragmentShader, fragmentSource);
    gl.compileShader(fragmentShader);

    if (!gl.getShaderParameter(fragmentShader, gl.COMPILE_STATUS)) {
      const log = gl.getShaderInfoLog(fragmentShader);
      gl.deleteShader(vertexShader);
      gl.deleteShader(fragmentShader);
      throw new Error(`Fragment shader compilation failed: ${log}`);
    }

    // Link program
    const program = gl.createProgram()!;
    gl.attachShader(program, vertexShader);
    gl.attachShader(program, fragmentShader);
    gl.linkProgram(program);

    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
      const log = gl.getProgramInfoLog(program);
      gl.deleteProgram(program);
      gl.deleteShader(vertexShader);
      gl.deleteShader(fragmentShader);
      throw new Error(`Program linking failed: ${log}`);
    }

    // Clean up shaders (they're attached to the program now)
    gl.deleteShader(vertexShader);
    gl.deleteShader(fragmentShader);

    return program;
  }

  renderScene(scene: Scene, frames: Map<string, VideoFrame | ImageBitmap>): void {
    const startTime = performance.now();
    const gl = this.gl;

    // Update textures from frames
    this.updateTextures(frames);

    // Clear with background color
    const bgColor = this.parseColor(scene.backgroundColor || '#000000');
    gl.clearColor(bgColor.r, bgColor.g, bgColor.b, 1.0);
    gl.clear(gl.COLOR_BUFFER_BIT);

    // Use layer program
    gl.useProgram(this.layerProgram);

    // Set resolution uniform
    gl.uniform2f(this.layerUniforms.resolution, this.config.width, this.config.height);

    // Sort layers by z-index and render
    const visibleLayers = scene.layers
      .filter((layer) => layer.visible)
      .sort((a, b) => a.zIndex - b.zIndex);

    for (const layer of visibleLayers) {
      const texture = this.textures.get(layer.sourceId);
      if (texture) {
        this.renderLayer(layer, texture);
      }
    }

    // Track stats
    this.lastRenderTime = performance.now() - startTime;
    this.updateStats();
  }

  resize(config: CompositorConfig): void {
    this.config = config;
    if (this.gl) {
      this.gl.viewport(0, 0, config.width, config.height);
    }
  }

  private updateTextures(frames: Map<string, VideoFrame | ImageBitmap>): void {
    const gl = this.gl;

    for (const [sourceId, frame] of frames) {
      let texture = this.textures.get(sourceId);
      const width = 'displayWidth' in frame ? frame.displayWidth : frame.width;
      const height = 'displayHeight' in frame ? frame.displayHeight : frame.height;
      const cached = this.textureCache.get(sourceId);

      if (!texture) {
        // Create new texture
        texture = gl.createTexture()!;
        gl.bindTexture(gl.TEXTURE_2D, texture);

        // Set texture parameters
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);

        this.textures.set(sourceId, texture);
      } else {
        gl.bindTexture(gl.TEXTURE_2D, texture);
      }

      // Upload frame data to texture
      // texImage2D can accept VideoFrame and ImageBitmap directly
      if (cached && cached.width === width && cached.height === height) {
        // Same size - use texSubImage2D for better performance
        gl.texSubImage2D(gl.TEXTURE_2D, 0, 0, 0, gl.RGBA, gl.UNSIGNED_BYTE, frame);
      } else {
        // Different size or new - use texImage2D
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, frame);
        this.textureCache.set(sourceId, { width, height });
      }
    }
  }

  private renderLayer(layer: Layer, texture: WebGLTexture): void {
    const gl = this.gl;
    const { x, y, width, height, opacity, rotation, borderRadius, crop } = layer.transform;
    const scalingMode = layer.scalingMode || 'letterbox';

    // Get texture dimensions for aspect ratio calculation
    const textureInfo = this.textureCache.get(layer.sourceId);
    if (!textureInfo) return;

    // Calculate source dimensions after user-defined crop
    const srcWidth = textureInfo.width * (1 - crop.left - crop.right);
    const srcHeight = textureInfo.height * (1 - crop.top - crop.bottom);
    const srcAspect = srcWidth / srcHeight;

    // Convert layer bounds to pixels for aspect calculation
    const destWidth = width * this.config.width;
    const destHeight = height * this.config.height;
    const destAspect = destWidth / destHeight;

    // Calculate scaled position, size, and crop based on scaling mode
    let finalX = x;
    let finalY = y;
    let finalWidth = width;
    let finalHeight = height;
    let finalCropLeft = crop.left;
    let finalCropTop = crop.top;
    let finalCropRight = crop.right;
    let finalCropBottom = crop.bottom;

    switch (scalingMode) {
      case 'stretch':
        // No changes - stretch to fill
        break;

      case 'letterbox': {
        // Fit source within destination, preserving aspect ratio
        let newWidth: number, newHeight: number;

        if (srcAspect > destAspect) {
          // Source is wider - fit to width, add top/bottom bars
          newWidth = width;
          newHeight = (width * this.config.width) / srcAspect / this.config.height;
        } else {
          // Source is taller - fit to height, add left/right bars
          newHeight = height;
          newWidth = (height * this.config.height * srcAspect) / this.config.width;
        }

        // Center within original bounds
        finalX = x + (width - newWidth) / 2;
        finalY = y + (height - newHeight) / 2;
        finalWidth = newWidth;
        finalHeight = newHeight;
        break;
      }

      case 'crop': {
        // Fill destination, preserving aspect ratio, crop source overflow
        if (srcAspect > destAspect) {
          // Source is wider - crop sides
          const targetSrcWidth = srcHeight * destAspect;
          const cropAmount = (srcWidth - targetSrcWidth) / 2 / textureInfo.width;
          finalCropLeft = crop.left + cropAmount;
          finalCropRight = crop.right + cropAmount;
        } else {
          // Source is taller - crop top/bottom
          const targetSrcHeight = srcWidth / destAspect;
          const cropAmount = (srcHeight - targetSrcHeight) / 2 / textureInfo.height;
          finalCropTop = crop.top + cropAmount;
          finalCropBottom = crop.bottom + cropAmount;
        }
        break;
      }
    }

    // Bind texture
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, texture);
    gl.uniform1i(this.layerUniforms.texture, 0);

    // Set transform uniforms with scaled values
    gl.uniform2f(this.layerUniforms.position, finalX, finalY);
    gl.uniform2f(this.layerUniforms.size, finalWidth, finalHeight);
    gl.uniform1f(this.layerUniforms.rotation, (rotation * Math.PI) / 180);
    gl.uniform4f(this.layerUniforms.crop, finalCropLeft, finalCropTop, finalCropRight, finalCropBottom);
    gl.uniform1f(this.layerUniforms.opacity, Math.max(0, Math.min(1, opacity)));
    gl.uniform1f(this.layerUniforms.borderRadius, borderRadius);

    // Set filter uniforms
    const filter = this.filterConfigs.get(layer.id);
    if (filter) {
      this.applyFilterUniforms(filter);
    } else {
      gl.uniform1i(this.layerUniforms.filterType, 0);
    }

    // Draw the quad (6 vertices for 2 triangles)
    gl.drawArrays(gl.TRIANGLES, 0, 6);
  }

  private applyFilterUniforms(filter: FilterConfig): void {
    const gl = this.gl;

    switch (filter.type) {
      case 'colorMatrix': {
        gl.uniform1i(this.layerUniforms.filterType, 2);
        // Build color matrix from filter settings
        const b = filter.brightness ?? 1;
        const c = filter.contrast ?? 1;
        const s = filter.saturation ?? 1;

        // Simplified color matrix combining brightness, contrast, saturation
        const sr = (1 - s) * 0.299;
        const sg = (1 - s) * 0.587;
        const sb = (1 - s) * 0.114;

        const matrix = new Float32Array([
          (sr + s) * c * b, sg * c * b, sb * c * b, 0,
          sr * c * b, (sg + s) * c * b, sb * c * b, 0,
          sr * c * b, sg * c * b, (sb + s) * c * b, 0,
          0, 0, 0, 1,
        ]);
        gl.uniformMatrix4fv(this.layerUniforms.colorMatrix, false, matrix);
        break;
      }

      case 'grayscale':
        gl.uniform1i(this.layerUniforms.filterType, 3);
        gl.uniform1f(this.layerUniforms.filterStrength, filter.strength ?? 1);
        break;

      case 'sepia':
        gl.uniform1i(this.layerUniforms.filterType, 4);
        gl.uniform1f(this.layerUniforms.filterStrength, filter.strength ?? 1);
        break;

      case 'blur':
        // Blur would require multi-pass rendering with framebuffers
        // For now, just log a warning
        console.warn('[WebGLRenderer] Blur filter requires multi-pass rendering, not yet implemented');
        gl.uniform1i(this.layerUniforms.filterType, 0);
        break;

      default:
        gl.uniform1i(this.layerUniforms.filterType, 0);
    }
  }

  renderTransition(
    from: ImageBitmap,
    to: ImageBitmap,
    progress: number,
    type: TransitionType
  ): void {
    const gl = this.gl;
    const p = Math.max(0, Math.min(1, progress));

    // Clear
    gl.clearColor(0, 0, 0, 1);
    gl.clear(gl.COLOR_BUFFER_BIT);

    // Use transition program
    gl.useProgram(this.transitionProgram);

    // Create temporary textures
    const fromTexture = this.createTempTexture(from);
    const toTexture = this.createTempTexture(to);

    switch (type) {
      case 'fade':
        // Draw "from" at decreasing opacity
        gl.activeTexture(gl.TEXTURE0);
        gl.bindTexture(gl.TEXTURE_2D, fromTexture);
        gl.uniform1i(this.transitionUniforms.texture, 0);
        gl.uniform1f(this.transitionUniforms.opacity, 1 - p);
        gl.uniform2f(this.transitionUniforms.offset, 0, 0);
        gl.drawArrays(gl.TRIANGLES, 0, 6);

        // Draw "to" at increasing opacity
        gl.bindTexture(gl.TEXTURE_2D, toTexture);
        gl.uniform1f(this.transitionUniforms.opacity, p);
        gl.drawArrays(gl.TRIANGLES, 0, 6);
        break;

      case 'slide-left': {
        const offset = p;
        // Draw "from" sliding out
        gl.bindTexture(gl.TEXTURE_2D, fromTexture);
        gl.uniform1f(this.transitionUniforms.opacity, 1);
        gl.uniform2f(this.transitionUniforms.offset, -offset, 0);
        gl.drawArrays(gl.TRIANGLES, 0, 6);

        // Draw "to" sliding in
        gl.bindTexture(gl.TEXTURE_2D, toTexture);
        gl.uniform2f(this.transitionUniforms.offset, 1 - offset, 0);
        gl.drawArrays(gl.TRIANGLES, 0, 6);
        break;
      }

      case 'slide-right': {
        const offset = p;
        gl.bindTexture(gl.TEXTURE_2D, fromTexture);
        gl.uniform1f(this.transitionUniforms.opacity, 1);
        gl.uniform2f(this.transitionUniforms.offset, offset, 0);
        gl.drawArrays(gl.TRIANGLES, 0, 6);

        gl.bindTexture(gl.TEXTURE_2D, toTexture);
        gl.uniform2f(this.transitionUniforms.offset, -1 + offset, 0);
        gl.drawArrays(gl.TRIANGLES, 0, 6);
        break;
      }

      case 'slide-up': {
        const offset = p;
        gl.bindTexture(gl.TEXTURE_2D, fromTexture);
        gl.uniform1f(this.transitionUniforms.opacity, 1);
        gl.uniform2f(this.transitionUniforms.offset, 0, offset);
        gl.drawArrays(gl.TRIANGLES, 0, 6);

        gl.bindTexture(gl.TEXTURE_2D, toTexture);
        gl.uniform2f(this.transitionUniforms.offset, 0, -1 + offset);
        gl.drawArrays(gl.TRIANGLES, 0, 6);
        break;
      }

      case 'slide-down': {
        const offset = p;
        gl.bindTexture(gl.TEXTURE_2D, fromTexture);
        gl.uniform1f(this.transitionUniforms.opacity, 1);
        gl.uniform2f(this.transitionUniforms.offset, 0, -offset);
        gl.drawArrays(gl.TRIANGLES, 0, 6);

        gl.bindTexture(gl.TEXTURE_2D, toTexture);
        gl.uniform2f(this.transitionUniforms.offset, 0, 1 - offset);
        gl.drawArrays(gl.TRIANGLES, 0, 6);
        break;
      }

      case 'cut':
      default:
        // Just draw the target
        gl.bindTexture(gl.TEXTURE_2D, toTexture);
        gl.uniform1f(this.transitionUniforms.opacity, 1);
        gl.uniform2f(this.transitionUniforms.offset, 0, 0);
        gl.drawArrays(gl.TRIANGLES, 0, 6);
        break;
    }

    // Clean up temp textures
    gl.deleteTexture(fromTexture);
    gl.deleteTexture(toTexture);
  }

  private createTempTexture(image: ImageBitmap): WebGLTexture {
    const gl = this.gl;
    const texture = gl.createTexture()!;
    gl.bindTexture(gl.TEXTURE_2D, texture);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, image);
    return texture;
  }

  applyFilter(layerId: string, filter: FilterConfig): void {
    this.filterConfigs.set(layerId, filter);
  }

  /**
   * Remove filters from a layer
   */
  clearFilter(layerId: string): void {
    this.filterConfigs.delete(layerId);
  }

  captureFrame(): VideoFrame {
    return new VideoFrame(this.canvas, {
      timestamp: performance.now() * 1000, // microseconds
    });
  }

  private parseColor(hex: string): { r: number; g: number; b: number } {
    // Parse hex color like '#000000' or '#000'
    let r = 0, g = 0, b = 0;

    if (hex.startsWith('#')) {
      hex = hex.slice(1);
    }

    if (hex.length === 3) {
      r = parseInt(hex[0] + hex[0], 16) / 255;
      g = parseInt(hex[1] + hex[1], 16) / 255;
      b = parseInt(hex[2] + hex[2], 16) / 255;
    } else if (hex.length === 6) {
      r = parseInt(hex.slice(0, 2), 16) / 255;
      g = parseInt(hex.slice(2, 4), 16) / 255;
      b = parseInt(hex.slice(4, 6), 16) / 255;
    }

    return { r, g, b };
  }

  private updateStats(): void {
    this.frameCount++;
    const now = performance.now();
    const elapsed = now - this.lastFrameTime;

    if (elapsed >= 1000) {
      this.fps = (this.frameCount * 1000) / elapsed;
      this.frameCount = 0;
      this.lastFrameTime = now;
    }
  }

  getStats(): RendererStats {
    return {
      fps: Math.round(this.fps),
      frameTimeMs: this.lastRenderTime,
    };
  }

  destroy(): void {
    const gl = this.gl;

    // Delete textures
    for (const texture of this.textures.values()) {
      gl.deleteTexture(texture);
    }
    this.textures.clear();
    this.textureCache.clear();

    // Delete programs
    if (this.layerProgram) {
      gl.deleteProgram(this.layerProgram);
    }
    if (this.transitionProgram) {
      gl.deleteProgram(this.transitionProgram);
    }

    // Clear filter configs
    this.filterConfigs.clear();

    // Lose context to free GPU resources
    const ext = gl.getExtension('WEBGL_lose_context');
    if (ext) {
      ext.loseContext();
    }
  }
}

// Register this renderer with the factory
registerRenderer('webgl', WebGLRenderer);
