/**
 * AudioWorklet global scope type declarations.
 *
 * The AudioWorklet spec defines its own global scope (AudioWorkletGlobalScope)
 * distinct from both Window and Worker. TypeScript has no built-in lib for it.
 * See: https://webaudio.github.io/web-audio-api/#AudioWorkletGlobalScope
 */

declare class AudioWorkletProcessor {
  readonly port: MessagePort;
  constructor();
  process(
    inputs: Float32Array[][],
    outputs: Float32Array[][],
    parameters: Record<string, Float32Array>
  ): boolean;
}

declare function registerProcessor(name: string, ctor: new () => AudioWorkletProcessor): void;

/** Current audio context time in seconds (AudioWorkletGlobalScope.currentTime) */
declare const currentTime: number;

/** Current sample rate (AudioWorkletGlobalScope.sampleRate) */
declare const sampleRate: number;

/** Current frame index (AudioWorkletGlobalScope.currentFrame) */
declare const currentFrame: number;
