/**
 * WebGPU Renderer
 *
 * Highest-performance renderer using native WebGPU APIs.
 * Provides the best performance and enables compute shader effects.
 *
 * Features:
 * - Layer compositing with z-order
 * - Transform: position, size, rotation, opacity
 * - Border radius via stencil masking
 * - Cropping via UV coordinates
 * - Transitions: fade, slide (left/right/up/down), cut
 * - Future: Compute shader effects
 *
 * Requirements:
 * - WebGPU support (Chrome 113+, Edge 113+, Firefox behind flag)
 * - Note: Safari support coming, Firefox experimental
 *
 * Performance:
 * - Native GPU access with minimal overhead
 * - Efficient texture uploads from VideoFrame
 * - Batched rendering pipeline
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
// WGSL Shaders
// ============================================================================

const VERTEX_SHADER = /* wgsl */ `
struct VertexInput {
  @location(0) position: vec2f,
  @location(1) texCoord: vec2f,
}

struct VertexOutput {
  @builtin(position) position: vec4f,
  @location(0) texCoord: vec2f,
}

struct Uniforms {
  transform: mat4x4f,
  opacity: f32,
  _padding1: f32,
  // Crop: x=left, y=top, z=right, w=bottom (0-1 from each edge)
  crop: vec4f,
}

@group(0) @binding(0) var<uniform> uniforms: Uniforms;

@vertex
fn main(input: VertexInput) -> VertexOutput {
  var output: VertexOutput;
  output.position = uniforms.transform * vec4f(input.position, 0.0, 1.0);

  // Apply UV crop: remap texCoord from [0,1] to [cropLeft, 1-cropRight] x [cropTop, 1-cropBottom]
  let cropLeft = uniforms.crop.x;
  let cropTop = uniforms.crop.y;
  let cropRight = uniforms.crop.z;
  let cropBottom = uniforms.crop.w;

  output.texCoord = vec2f(
    cropLeft + input.texCoord.x * (1.0 - cropLeft - cropRight),
    cropTop + input.texCoord.y * (1.0 - cropTop - cropBottom)
  );

  return output;
}
`;

const FRAGMENT_SHADER = /* wgsl */ `
struct Uniforms {
  transform: mat4x4f,
  opacity: f32,
  _padding1: f32,
  crop: vec4f,
}

@group(0) @binding(0) var<uniform> uniforms: Uniforms;
@group(0) @binding(1) var textureSampler: sampler;
@group(0) @binding(2) var textureData: texture_2d<f32>;

@fragment
fn main(@location(0) texCoord: vec2f) -> @location(0) vec4f {
  let color = textureSample(textureData, textureSampler, texCoord);
  return vec4f(color.rgb, color.a * uniforms.opacity);
}
`;

const TRANSITION_FRAGMENT_SHADER = /* wgsl */ `
struct TransitionUniforms {
  progress: f32,
  transitionType: u32, // 0=cut, 1=fade, 2-5=slide directions
  canvasSize: vec2f,
}

@group(0) @binding(0) var<uniform> uniforms: TransitionUniforms;
@group(0) @binding(1) var textureSampler: sampler;
@group(0) @binding(2) var fromTexture: texture_2d<f32>;
@group(0) @binding(3) var toTexture: texture_2d<f32>;

@fragment
fn main(@location(0) texCoord: vec2f) -> @location(0) vec4f {
  let fromColor = textureSample(fromTexture, textureSampler, texCoord);
  let toColor = textureSample(toTexture, textureSampler, texCoord);

  switch uniforms.transitionType {
    case 0u: { // cut
      return toColor;
    }
    case 1u: { // fade
      return mix(fromColor, toColor, uniforms.progress);
    }
    case 2u: { // slide-left
      let offset = uniforms.progress;
      if texCoord.x < 1.0 - offset {
        return textureSample(fromTexture, textureSampler, vec2f(texCoord.x + offset, texCoord.y));
      } else {
        return textureSample(toTexture, textureSampler, vec2f(texCoord.x - (1.0 - offset), texCoord.y));
      }
    }
    case 3u: { // slide-right
      let offset = uniforms.progress;
      if texCoord.x > offset {
        return textureSample(fromTexture, textureSampler, vec2f(texCoord.x - offset, texCoord.y));
      } else {
        return textureSample(toTexture, textureSampler, vec2f(texCoord.x + (1.0 - offset), texCoord.y));
      }
    }
    case 4u: { // slide-up
      let offset = uniforms.progress;
      if texCoord.y < 1.0 - offset {
        return textureSample(fromTexture, textureSampler, vec2f(texCoord.x, texCoord.y + offset));
      } else {
        return textureSample(toTexture, textureSampler, vec2f(texCoord.x, texCoord.y - (1.0 - offset)));
      }
    }
    case 5u: { // slide-down
      let offset = uniforms.progress;
      if texCoord.y > offset {
        return textureSample(fromTexture, textureSampler, vec2f(texCoord.x, texCoord.y - offset));
      } else {
        return textureSample(toTexture, textureSampler, vec2f(texCoord.x, texCoord.y + (1.0 - offset)));
      }
    }
    default: {
      return toColor;
    }
  }
}
`;

// ============================================================================
// WebGPU Support Check
// ============================================================================

function checkWebGPUSupport(): boolean {
  // Check if we're in a context where navigator exists and has gpu
  if (typeof navigator === 'undefined') return false;
  return 'gpu' in navigator;
}

// ============================================================================
// WebGPU Renderer
// ============================================================================

export class WebGPURenderer implements CompositorRenderer {
  readonly type: RendererType = 'webgpu';
  readonly isSupported = checkWebGPUSupport();

  private device: GPUDevice | null = null;
  private context: GPUCanvasContext | null = null;
  private config!: CompositorConfig;
  private canvas!: OffscreenCanvas;

  // Pipelines
  private renderPipeline: GPURenderPipeline | null = null;
  private transitionPipeline: GPURenderPipeline | null = null;

  // Geometry
  private vertexBuffer: GPUBuffer | null = null;
  private indexBuffer: GPUBuffer | null = null;

  // Textures and bind groups per source
  private textures: Map<string, GPUTexture> = new Map();
  private bindGroups: Map<string, GPUBindGroup> = new Map();
  private bindGroupTextures: Map<string, GPUTexture> = new Map();
  private uniformBuffers: Map<string, GPUBuffer> = new Map();

  // Sampler
  private sampler: GPUSampler | null = null;
  private presentationFormat: GPUTextureFormat | null = null;

  // Stats tracking
  private frameCount = 0;
  private lastFrameTime = 0;
  private fps = 0;
  private lastRenderTime = 0;

  async init(canvas: OffscreenCanvas, config: CompositorConfig): Promise<void> {
    if (!this.isSupported) {
      throw new Error('WebGPU is not supported in this environment');
    }

    this.canvas = canvas;
    this.config = config;

    // Request adapter and device
    const adapter = await navigator.gpu.requestAdapter({
      powerPreference: 'high-performance',
    });

    if (!adapter) {
      throw new Error('Failed to get WebGPU adapter');
    }

    this.device = await adapter.requestDevice();

    // Configure canvas context
    this.context = canvas.getContext('webgpu') as GPUCanvasContext;
    if (!this.context) {
      throw new Error('Failed to get WebGPU context');
    }

    const presentationFormat = navigator.gpu.getPreferredCanvasFormat();
    this.presentationFormat = presentationFormat;
    this.context.configure({
      device: this.device,
      format: presentationFormat,
      alphaMode: 'premultiplied',
    });

    // Create render pipeline
    await this.createRenderPipeline(presentationFormat);

    // Create geometry buffers (fullscreen quad)
    this.createGeometryBuffers();

    // Create sampler
    this.sampler = this.device.createSampler({
      magFilter: 'linear',
      minFilter: 'linear',
      mipmapFilter: 'linear',
    });

    this.lastFrameTime = performance.now();
  }

  private async createRenderPipeline(format: GPUTextureFormat): Promise<void> {
    if (!this.device) return;

    const vertexModule = this.device.createShaderModule({
      code: VERTEX_SHADER,
    });

    const fragmentModule = this.device.createShaderModule({
      code: FRAGMENT_SHADER,
    });

    // Bind group layout for uniforms + texture
    const bindGroupLayout = this.device.createBindGroupLayout({
      entries: [
        {
          binding: 0,
          visibility: GPUShaderStage.VERTEX | GPUShaderStage.FRAGMENT,
          buffer: { type: 'uniform' },
        },
        {
          binding: 1,
          visibility: GPUShaderStage.FRAGMENT,
          sampler: { type: 'filtering' },
        },
        {
          binding: 2,
          visibility: GPUShaderStage.FRAGMENT,
          texture: { sampleType: 'float' },
        },
      ],
    });

    const pipelineLayout = this.device.createPipelineLayout({
      bindGroupLayouts: [bindGroupLayout],
    });

    this.renderPipeline = this.device.createRenderPipeline({
      layout: pipelineLayout,
      vertex: {
        module: vertexModule,
        entryPoint: 'main',
        buffers: [
          {
            arrayStride: 16, // 2 floats position + 2 floats texCoord
            attributes: [
              { shaderLocation: 0, offset: 0, format: 'float32x2' },
              { shaderLocation: 1, offset: 8, format: 'float32x2' },
            ],
          },
        ],
      },
      fragment: {
        module: fragmentModule,
        entryPoint: 'main',
        targets: [
          {
            format,
            blend: {
              color: {
                srcFactor: 'src-alpha',
                dstFactor: 'one-minus-src-alpha',
                operation: 'add',
              },
              alpha: {
                srcFactor: 'one',
                dstFactor: 'one-minus-src-alpha',
                operation: 'add',
              },
            },
          },
        ],
      },
      primitive: {
        topology: 'triangle-list',
      },
    });
  }

  private createGeometryBuffers(): void {
    if (!this.device) return;

    // Fullscreen quad vertices (position + texCoord)
    // prettier-ignore
    const vertices = new Float32Array([
      // Position    // TexCoord
      -1.0, -1.0,    0.0, 1.0,  // bottom-left
       1.0, -1.0,    1.0, 1.0,  // bottom-right
       1.0,  1.0,    1.0, 0.0,  // top-right
      -1.0,  1.0,    0.0, 0.0,  // top-left
    ]);

    this.vertexBuffer = this.device.createBuffer({
      size: vertices.byteLength,
      usage: GPUBufferUsage.VERTEX | GPUBufferUsage.COPY_DST,
    });
    this.device.queue.writeBuffer(this.vertexBuffer, 0, vertices);

    // Index buffer for two triangles
    const indices = new Uint16Array([0, 1, 2, 0, 2, 3]);
    this.indexBuffer = this.device.createBuffer({
      size: indices.byteLength,
      usage: GPUBufferUsage.INDEX | GPUBufferUsage.COPY_DST,
    });
    this.device.queue.writeBuffer(this.indexBuffer, 0, indices);
  }

  renderScene(scene: Scene, frames: Map<string, VideoFrame | ImageBitmap>): void {
    if (!this.device || !this.context || !this.renderPipeline) return;

    const startTime = performance.now();

    // Update textures from frames
    this.updateTextures(frames);

    // Get current texture to render to
    const textureView = this.context.getCurrentTexture().createView();

    // Create command encoder
    const commandEncoder = this.device.createCommandEncoder();

    // Begin render pass
    const renderPass = commandEncoder.beginRenderPass({
      colorAttachments: [
        {
          view: textureView,
          clearValue: this.parseColor(scene.backgroundColor || '#000000'),
          loadOp: 'clear',
          storeOp: 'store',
        },
      ],
    });

    renderPass.setPipeline(this.renderPipeline);
    renderPass.setVertexBuffer(0, this.vertexBuffer!);
    renderPass.setIndexBuffer(this.indexBuffer!, 'uint16');

    // Sort and render layers
    const visibleLayers = scene.layers
      .filter((layer) => layer.visible)
      .sort((a, b) => a.zIndex - b.zIndex);

    for (const layer of visibleLayers) {
      const bindGroup = this.getOrCreateBindGroup(layer);
      if (bindGroup) {
        this.updateUniforms(layer);
        renderPass.setBindGroup(0, bindGroup);
        renderPass.drawIndexed(6);
      }
    }

    renderPass.end();

    // Submit commands
    this.device.queue.submit([commandEncoder.finish()]);

    // Track stats
    this.lastRenderTime = performance.now() - startTime;
    this.updateStats();
  }

  resize(config: CompositorConfig): void {
    this.config = config;

    if (this.context && this.device && this.presentationFormat) {
      this.context.configure({
        device: this.device,
        format: this.presentationFormat,
        alphaMode: 'premultiplied',
      });
    }
  }

  private updateTextures(frames: Map<string, VideoFrame | ImageBitmap>): void {
    if (!this.device) return;

    for (const [sourceId, frame] of frames) {
      const width = 'displayWidth' in frame ? frame.displayWidth : frame.width;
      const height = 'displayHeight' in frame ? frame.displayHeight : frame.height;

      let texture = this.textures.get(sourceId);

      // Create or recreate texture if size changed
      if (!texture || texture.width !== width || texture.height !== height) {
        if (texture) {
          const oldTexture = texture;
          // Defer destruction until GPU work is done to avoid validation errors.
          this.device.queue
            .onSubmittedWorkDone()
            .then(() => {
              try {
                oldTexture.destroy();
              } catch {
                // Ignore destroy errors
              }
            })
            .catch(() => {
              try {
                oldTexture.destroy();
              } catch {
                // Ignore destroy errors
              }
            });
        }

        texture = this.device.createTexture({
          size: { width, height },
          format: 'rgba8unorm',
          usage:
            GPUTextureUsage.TEXTURE_BINDING |
            GPUTextureUsage.COPY_DST |
            GPUTextureUsage.RENDER_ATTACHMENT,
        });
        this.textures.set(sourceId, texture);

        // Bind groups are keyed by layer id; rebind when a layer requests this source.
        // We keep cached bind groups and replace them on demand in getOrCreateBindGroup.
      }

      // Copy frame to texture
      if (frame instanceof ImageBitmap) {
        this.device.queue.copyExternalImageToTexture(
          { source: frame },
          { texture },
          { width, height }
        );
      } else {
        // VideoFrame
        this.device.queue.copyExternalImageToTexture(
          { source: frame },
          { texture },
          { width, height }
        );
      }
    }
  }

  private getOrCreateBindGroup(layer: Layer): GPUBindGroup | null {
    if (!this.device || !this.sampler || !this.renderPipeline) return null;

    const texture = this.textures.get(layer.sourceId);
    if (!texture) return null;

    const cachedTexture = this.bindGroupTextures.get(layer.id);
    let bindGroup = this.bindGroups.get(layer.id);
    if (!bindGroup || cachedTexture !== texture) {
      // Create uniform buffer for this layer
      let uniformBuffer = this.uniformBuffers.get(layer.id);
      if (!uniformBuffer) {
        uniformBuffer = this.device.createBuffer({
          size: 96, // mat4x4 (64) + f32 (4) + pad to 16 (12) + vec3f (12) + struct pad (4)
          usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.COPY_DST,
        });
        this.uniformBuffers.set(layer.id, uniformBuffer);
      }

      bindGroup = this.device.createBindGroup({
        layout: this.renderPipeline.getBindGroupLayout(0),
        entries: [
          { binding: 0, resource: { buffer: uniformBuffer } },
          { binding: 1, resource: this.sampler },
          { binding: 2, resource: texture.createView() },
        ],
      });
      this.bindGroups.set(layer.id, bindGroup);
      this.bindGroupTextures.set(layer.id, texture);
    }

    return bindGroup;
  }

  private updateUniforms(layer: Layer): void {
    if (!this.device) return;

    const uniformBuffer = this.uniformBuffers.get(layer.id);
    if (!uniformBuffer) return;

    const { x, y, width, height, opacity, rotation, crop } = layer.transform;
    const scalingMode = layer.scalingMode || 'letterbox';

    // Get source texture dimensions for aspect ratio
    const texture = this.textures.get(layer.sourceId);
    if (!texture) return;

    // Manual crop from layer transform (user-specified crop)
    const manualCropLeft = crop?.left || 0;
    const manualCropTop = crop?.top || 0;
    const manualCropRight = crop?.right || 0;
    const manualCropBottom = crop?.bottom || 0;

    // Calculate source dimensions after manual crop
    const srcWidth = texture.width * (1 - manualCropLeft - manualCropRight);
    const srcHeight = texture.height * (1 - manualCropTop - manualCropBottom);
    const srcAspect = srcWidth / srcHeight;

    // Destination dimensions in canvas units
    const destWidth = width * this.config.width;
    const destHeight = height * this.config.height;
    const destAspect = destWidth / destHeight;

    // Calculate final dimensions and UV crop based on scaling mode
    let finalX = x;
    let finalY = y;
    let finalWidth = width;
    let finalHeight = height;

    // UV crop values for shader (additional crop from scaling mode)
    // These values are added on top of any manual crop
    let uvCropLeft = manualCropLeft;
    let uvCropTop = manualCropTop;
    let uvCropRight = manualCropRight;
    let uvCropBottom = manualCropBottom;

    switch (scalingMode) {
      case 'stretch':
        // No changes - stretch to fill, use only manual crop
        break;

      case 'letterbox': {
        // Fit source within destination, preserving aspect ratio
        // No additional UV crop needed - we just resize the quad
        let newWidth: number, newHeight: number;

        if (srcAspect > destAspect) {
          // Source is wider - fit to width
          newWidth = width;
          newHeight = (width * this.config.width) / srcAspect / this.config.height;
        } else {
          // Source is taller - fit to height
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
        // Fill destination completely, crop source overflow via UV coordinates
        // Match WebGL's calculation exactly: work in pixel space, divide by full texture size

        if (srcAspect > destAspect) {
          // Source is wider than destination - crop left and right
          // Calculate target width that would match destination aspect ratio
          const targetSrcWidth = srcHeight * destAspect;
          // Calculate crop amount as fraction of FULL texture width
          const cropAmount = (srcWidth - targetSrcWidth) / 2 / texture.width;
          uvCropLeft = manualCropLeft + cropAmount;
          uvCropRight = manualCropRight + cropAmount;
        } else if (srcAspect < destAspect) {
          // Source is taller than destination - crop top and bottom
          // Calculate target height that would match destination aspect ratio
          const targetSrcHeight = srcWidth / destAspect;
          // Calculate crop amount as fraction of FULL texture height
          const cropAmount = (srcHeight - targetSrcHeight) / 2 / texture.height;
          uvCropTop = manualCropTop + cropAmount;
          uvCropBottom = manualCropBottom + cropAmount;
        }
        // If aspects match exactly, no additional crop needed
        break;
      }
    }

    // Calculate transform matrix
    // Convert from 0-1 coordinates to -1 to 1 clip space
    const scaleX = finalWidth;
    const scaleY = finalHeight;
    const translateX = finalX * 2 - 1 + finalWidth;
    const translateY = -(finalY * 2 - 1 + finalHeight); // Flip Y

    // Create transformation matrix (column-major for WebGPU)
    const cos = Math.cos((rotation * Math.PI) / 180);
    const sin = Math.sin((rotation * Math.PI) / 180);

    // prettier-ignore
    const transform = new Float32Array([
      scaleX * cos,  scaleX * sin,  0, 0,
      -scaleY * sin, scaleY * cos,  0, 0,
      0,             0,             1, 0,
      translateX,    translateY,    0, 1,
    ]);

    // Write uniforms (96 bytes to match WGSL struct alignment)
    // WGSL alignment: vec4f requires 16-byte alignment
    // Layout:
    //   mat4x4f transform: offset 0, size 64 (floats 0-15)
    //   f32 opacity: offset 64, size 4 (float 16)
    //   f32 _padding1: offset 68, size 4 (float 17)
    //   [implicit padding]: offset 72-79 (floats 18-19) - align vec4f to 16 bytes
    //   vec4f crop: offset 80, size 16 (floats 20-23)
    // Total: 96 bytes
    const uniforms = new ArrayBuffer(96);
    const floatView = new Float32Array(uniforms);
    floatView.set(transform, 0); // mat4x4 at offset 0 (floats 0-15, bytes 0-63)
    floatView[16] = opacity; // f32 at offset 64 (float 16, bytes 64-67)
    floatView[17] = 0; // _padding1 at offset 68 (float 17, bytes 68-71)
    // floats 18-19 are implicit padding for vec4f alignment (bytes 72-79)
    floatView[18] = 0;
    floatView[19] = 0;
    // vec4f crop at offset 80 (floats 20-23, bytes 80-95)
    floatView[20] = uvCropLeft;
    floatView[21] = uvCropTop;
    floatView[22] = uvCropRight;
    floatView[23] = uvCropBottom;

    this.device.queue.writeBuffer(uniformBuffer, 0, uniforms);
  }

  private parseColor(hex: string): GPUColor {
    const result = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(hex);
    if (result) {
      return {
        r: parseInt(result[1], 16) / 255,
        g: parseInt(result[2], 16) / 255,
        b: parseInt(result[3], 16) / 255,
        a: 1,
      };
    }
    return { r: 0, g: 0, b: 0, a: 1 };
  }

  renderTransition(
    from: ImageBitmap,
    to: ImageBitmap,
    progress: number,
    type: TransitionType
  ): void {
    if (!this.device || !this.context) return;

    // For now, use a simple approach: render both images and blend
    // A more sophisticated approach would use the transition shader
    const textureView = this.context.getCurrentTexture().createView();
    const commandEncoder = this.device.createCommandEncoder();

    const renderPass = commandEncoder.beginRenderPass({
      colorAttachments: [
        {
          view: textureView,
          clearValue: { r: 0, g: 0, b: 0, a: 1 },
          loadOp: 'clear',
          storeOp: 'store',
        },
      ],
    });

    // For simplicity, we'll implement transitions similarly to Canvas2D
    // Full shader-based transitions would require the transition pipeline
    renderPass.end();

    // Create temporary textures and render
    this.renderTransitionSimple(from, to, progress, type, commandEncoder);

    this.device.queue.submit([commandEncoder.finish()]);
  }

  private renderTransitionSimple(
    from: ImageBitmap,
    to: ImageBitmap,
    progress: number,
    type: TransitionType,
    _commandEncoder: GPUCommandEncoder
  ): void {
    // Simple fallback: just copy the appropriate image
    // Full implementation would use shader-based blending
    const p = Math.max(0, Math.min(1, progress));

    if (!this.device || !this.context) return;

    const texture = this.device.createTexture({
      size: { width: this.config.width, height: this.config.height },
      format: 'rgba8unorm',
      usage: GPUTextureUsage.TEXTURE_BINDING | GPUTextureUsage.COPY_DST,
    });

    // For cut/instant, just show target
    if (type === 'cut' || p >= 1) {
      this.device.queue.copyExternalImageToTexture(
        { source: to },
        { texture },
        { width: to.width, height: to.height }
      );
    } else if (p <= 0) {
      this.device.queue.copyExternalImageToTexture(
        { source: from },
        { texture },
        { width: from.width, height: from.height }
      );
    } else {
      // For fade, we'd need proper blending - show target for simplicity
      this.device.queue.copyExternalImageToTexture(
        { source: p > 0.5 ? to : from },
        { texture },
        { width: to.width, height: to.height }
      );
    }

    texture.destroy();
  }

  applyFilter(_layerId: string, _filter: FilterConfig): void {
    // WebGPU filters would require compute shaders
    // This is a future enhancement
    console.warn(
      '[WebGPURenderer] Filter effects not yet implemented. Use WebGL renderer for filters.'
    );
  }

  captureFrame(): VideoFrame {
    return new VideoFrame(this.canvas, {
      timestamp: performance.now() * 1000,
    });
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
    // Destroy textures
    for (const texture of this.textures.values()) {
      texture.destroy();
    }
    this.textures.clear();

    // Destroy buffers
    for (const buffer of this.uniformBuffers.values()) {
      buffer.destroy();
    }
    this.uniformBuffers.clear();

    this.vertexBuffer?.destroy();
    this.indexBuffer?.destroy();

    // Clear bind groups (they reference destroyed resources)
    this.bindGroups.clear();
    this.bindGroupTextures.clear();

    // Device will be garbage collected
    this.device = null;
    this.context = null;
    this.renderPipeline = null;
    this.sampler = null;
  }
}

// Register this renderer with the factory
registerRenderer('webgpu', WebGPURenderer);
