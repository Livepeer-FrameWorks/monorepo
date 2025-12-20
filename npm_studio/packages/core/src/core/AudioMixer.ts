/**
 * Audio Mixer
 * Combines multiple audio tracks using Web Audio API
 * Supports per-source volume control, muting, and panning
 */

import { TypedEventEmitter } from './EventEmitter';
import type { AudioMixerConfig, AudioMixerEvents, AudioSourceOptions } from '../types';

interface AudioSourceNode {
  id: string;
  sourceNode: MediaStreamAudioSourceNode;
  gainNode: GainNode;
  panNode: StereoPannerNode;
  track: MediaStreamTrack;
  options: Required<AudioSourceOptions>;
}

export class AudioMixer extends TypedEventEmitter<AudioMixerEvents> {
  private audioContext: AudioContext | null = null;
  private destination: MediaStreamAudioDestinationNode | null = null;
  private masterGain: GainNode | null = null;
  private compressor: DynamicsCompressorNode | null = null;
  private limiter: DynamicsCompressorNode | null = null;
  private analyzer: AnalyserNode | null = null;
  private sources: Map<string, AudioSourceNode> = new Map();
  private config: Required<AudioMixerConfig>;
  private outputStream: MediaStream | null = null;
  private levelMonitoringActive = false;
  private peakLevel = 0;
  private peakDecayRate = 0.95; // Peak meter decay

  constructor(config: AudioMixerConfig = {}) {
    super();
    this.config = {
      sampleRate: config.sampleRate ?? 48000,
      channelCount: config.channelCount ?? 2,
    };
  }

  /**
   * Initialize the audio mixer
   */
  async initialize(): Promise<void> {
    if (this.audioContext) {
      return; // Already initialized
    }

    try {
      this.audioContext = new AudioContext({
        sampleRate: this.config.sampleRate,
      });

      // Create destination node (outputs to MediaStream)
      this.destination = this.audioContext.createMediaStreamDestination();
      this.destination.channelCount = this.config.channelCount;

      // Create master gain node
      this.masterGain = this.audioContext.createGain();

      // Create compressor for consistent levels (voice/streaming optimized)
      this.compressor = this.audioContext.createDynamicsCompressor();
      this.compressor.threshold.value = -24; // Start compressing at -24dB
      this.compressor.knee.value = 30; // Soft knee for natural sound
      this.compressor.ratio.value = 4; // 4:1 compression
      this.compressor.attack.value = 0.003; // 3ms attack (fast for peaks)
      this.compressor.release.value = 0.25; // 250ms release

      // Create limiter (prevent clipping, brick-wall at -1dB)
      this.limiter = this.audioContext.createDynamicsCompressor();
      this.limiter.threshold.value = -1; // Brick-wall at -1dB
      this.limiter.knee.value = 0; // Hard knee
      this.limiter.ratio.value = 20; // Heavy limiting
      this.limiter.attack.value = 0.001; // 1ms attack
      this.limiter.release.value = 0.1; // 100ms release

      // Create analyzer for VU meter (non-destructive tap)
      this.analyzer = this.audioContext.createAnalyser();
      this.analyzer.fftSize = 256;
      this.analyzer.smoothingTimeConstant = 0.3;

      // Connect the chain: masterGain -> compressor -> analyzer -> limiter -> destination
      // Analyzer is after compressor so we see post-compression levels
      this.masterGain.connect(this.compressor);
      this.compressor.connect(this.analyzer);
      this.analyzer.connect(this.limiter);
      this.limiter.connect(this.destination);

      // Get the output stream
      this.outputStream = this.destination.stream;

      console.log('[AudioMixer] Initialized with compressor/limiter chain', {
        sampleRate: this.audioContext.sampleRate,
        channelCount: this.config.channelCount,
      });
    } catch (error) {
      const err = error instanceof Error ? error : new Error(String(error));
      console.error('[AudioMixer] Failed to initialize:', err);
      this.emit('error', { message: err.message, error: err });
      throw err;
    }
  }

  /**
   * Add an audio source to the mixer
   */
  addSource(
    id: string,
    track: MediaStreamTrack,
    options: AudioSourceOptions = {}
  ): void {
    if (!this.audioContext || !this.masterGain) {
      throw new Error('AudioMixer not initialized. Call initialize() first.');
    }

    if (track.kind !== 'audio') {
      throw new Error('Track must be an audio track');
    }

    // Remove existing source with same ID if present
    if (this.sources.has(id)) {
      this.removeSource(id);
    }

    try {
      // Create a MediaStream from the track
      const stream = new MediaStream([track]);

      // Create source node from the stream
      const sourceNode = this.audioContext.createMediaStreamSource(stream);

      // Create gain node for volume control
      const gainNode = this.audioContext.createGain();
      gainNode.gain.value = options.muted ? 0 : (options.volume ?? 1.0);

      // Create panner node for stereo positioning
      const panNode = this.audioContext.createStereoPanner();
      panNode.pan.value = options.pan ?? 0;

      // Connect the chain: source -> gain -> pan -> master
      sourceNode.connect(gainNode);
      gainNode.connect(panNode);
      panNode.connect(this.masterGain);

      // Store the source
      const audioSource: AudioSourceNode = {
        id,
        sourceNode,
        gainNode,
        panNode,
        track,
        options: {
          volume: options.volume ?? 1.0,
          muted: options.muted ?? false,
          pan: options.pan ?? 0,
        },
      };

      this.sources.set(id, audioSource);

      console.log('[AudioMixer] Added source:', id);
      this.emit('sourceAdded', { sourceId: id });
    } catch (error) {
      const err = error instanceof Error ? error : new Error(String(error));
      console.error('[AudioMixer] Failed to add source:', err);
      this.emit('error', { message: `Failed to add source: ${err.message}`, error: err });
      throw err;
    }
  }

  /**
   * Remove an audio source from the mixer
   */
  removeSource(id: string): void {
    const source = this.sources.get(id);
    if (!source) {
      return;
    }

    try {
      // Disconnect all nodes
      source.sourceNode.disconnect();
      source.gainNode.disconnect();
      source.panNode.disconnect();

      this.sources.delete(id);

      console.log('[AudioMixer] Removed source:', id);
      this.emit('sourceRemoved', { sourceId: id });
    } catch (error) {
      console.error('[AudioMixer] Error removing source:', error);
    }
  }

  /**
   * Update source options
   */
  updateSource(id: string, options: AudioSourceOptions): void {
    const source = this.sources.get(id);
    if (!source) {
      console.warn('[AudioMixer] Source not found:', id);
      return;
    }

    // Update volume
    if (options.volume !== undefined) {
      source.options.volume = options.volume;
      if (!source.options.muted) {
        source.gainNode.gain.setTargetAtTime(
          options.volume,
          this.audioContext?.currentTime ?? 0,
          0.01 // Smooth transition
        );
      }
    }

    // Update mute state
    if (options.muted !== undefined) {
      source.options.muted = options.muted;
      source.gainNode.gain.setTargetAtTime(
        options.muted ? 0 : source.options.volume,
        this.audioContext?.currentTime ?? 0,
        0.01
      );
    }

    // Update pan
    if (options.pan !== undefined) {
      source.options.pan = options.pan;
      source.panNode.pan.setTargetAtTime(
        options.pan,
        this.audioContext?.currentTime ?? 0,
        0.01
      );
    }
  }

  /**
   * Set volume for a source (supports boost up to 2x)
   */
  setVolume(id: string, volume: number): void {
    // Allow gain boost up to 2.0 (double) for quiet sources
    this.updateSource(id, { volume: Math.max(0, Math.min(2.0, volume)) });
  }

  /**
   * Mute a source
   */
  mute(id: string): void {
    this.updateSource(id, { muted: true });
  }

  /**
   * Unmute a source
   */
  unmute(id: string): void {
    this.updateSource(id, { muted: false });
  }

  /**
   * Toggle mute for a source
   */
  toggleMute(id: string): boolean {
    const source = this.sources.get(id);
    if (!source) {
      return false;
    }
    const newMuted = !source.options.muted;
    this.updateSource(id, { muted: newMuted });
    return newMuted;
  }

  /**
   * Set pan for a source (-1.0 = left, 0 = center, 1.0 = right)
   */
  setPan(id: string, pan: number): void {
    this.updateSource(id, { pan: Math.max(-1, Math.min(1, pan)) });
  }

  /**
   * Set master volume (supports boost up to 2x / +6dB)
   */
  setMasterVolume(volume: number): void {
    if (!this.masterGain) return;
    this.masterGain.gain.setTargetAtTime(
      Math.max(0, Math.min(2, volume)),
      this.audioContext?.currentTime ?? 0,
      0.01
    );
  }

  /**
   * Get current master volume
   */
  getMasterVolume(): number {
    return this.masterGain?.gain.value ?? 1;
  }

  /**
   * Get the mixed output stream
   */
  getOutputStream(): MediaStream | null {
    return this.outputStream;
  }

  /**
   * Get the output audio track
   */
  getOutputTrack(): MediaStreamTrack | null {
    return this.outputStream?.getAudioTracks()[0] ?? null;
  }

  /**
   * Get all source IDs
   */
  getSourceIds(): string[] {
    return Array.from(this.sources.keys());
  }

  /**
   * Get source options
   */
  getSourceOptions(id: string): Required<AudioSourceOptions> | null {
    const source = this.sources.get(id);
    return source ? { ...source.options } : null;
  }

  /**
   * Check if a source exists
   */
  hasSource(id: string): boolean {
    return this.sources.has(id);
  }

  /**
   * Get the number of sources
   */
  getSourceCount(): number {
    return this.sources.size;
  }

  /**
   * Resume audio context (required after user interaction)
   */
  async resume(): Promise<void> {
    if (this.audioContext && this.audioContext.state === 'suspended') {
      await this.audioContext.resume();
    }
  }

  /**
   * Suspend audio context
   */
  async suspend(): Promise<void> {
    if (this.audioContext && this.audioContext.state === 'running') {
      await this.audioContext.suspend();
    }
  }

  /**
   * Get audio context state
   */
  getState(): AudioContextState | null {
    return this.audioContext?.state ?? null;
  }

  /**
   * Get current audio level (0-1) for VU meter
   * Uses dB scaling for perceptually accurate display
   */
  getLevel(): number {
    if (!this.analyzer) return 0;

    const data = new Uint8Array(this.analyzer.frequencyBinCount);
    this.analyzer.getByteTimeDomainData(data);

    // Calculate peak amplitude (linear 0-1)
    let max = 0;
    for (let i = 0; i < data.length; i++) {
      const v = Math.abs(data[i] - 128) / 128;
      if (v > max) max = v;
    }

    // Avoid log(0)
    if (max < 0.0001) return 0;

    // Convert to dB scale
    // 0 dB = full scale (max=1), -60 dB = very quiet
    const dB = 20 * Math.log10(max);

    // Map dB range to 0-1
    // -60 dB -> 0, 0 dB -> 1
    const minDb = -60;
    const maxDb = 0;
    const normalized = (dB - minDb) / (maxDb - minDb);

    return Math.max(0, Math.min(1, normalized));
  }

  /**
   * Get current and peak audio levels for VU meter with decay
   */
  getLevels(): { level: number; peakLevel: number } {
    const level = this.getLevel();

    // Update peak with decay
    if (level > this.peakLevel) {
      this.peakLevel = level;
    } else {
      this.peakLevel *= this.peakDecayRate;
    }

    return { level, peakLevel: this.peakLevel };
  }

  /**
   * Start emitting level updates (for VU meter)
   * Call this when you want to start monitoring audio levels
   */
  startLevelMonitoring(): void {
    if (this.levelMonitoringActive) return;
    this.levelMonitoringActive = true;

    const update = () => {
      if (!this.levelMonitoringActive || this.audioContext?.state !== 'running') {
        return;
      }

      const { level, peakLevel } = this.getLevels();
      this.emit('levelUpdate', { level, peakLevel });

      requestAnimationFrame(update);
    };

    requestAnimationFrame(update);
    console.log('[AudioMixer] Level monitoring started');
  }

  /**
   * Stop emitting level updates
   */
  stopLevelMonitoring(): void {
    this.levelMonitoringActive = false;
    this.peakLevel = 0;
    console.log('[AudioMixer] Level monitoring stopped');
  }

  /**
   * Check if level monitoring is active
   */
  isMonitoringLevels(): boolean {
    return this.levelMonitoringActive;
  }

  /**
   * Destroy the mixer and clean up resources
   */
  destroy(): void {
    // Stop level monitoring
    this.stopLevelMonitoring();

    // Remove all sources
    for (const id of this.sources.keys()) {
      this.removeSource(id);
    }

    // Disconnect processing nodes
    if (this.compressor) {
      this.compressor.disconnect();
      this.compressor = null;
    }

    if (this.limiter) {
      this.limiter.disconnect();
      this.limiter = null;
    }

    if (this.analyzer) {
      this.analyzer.disconnect();
      this.analyzer = null;
    }

    // Close audio context
    if (this.audioContext) {
      this.audioContext.close().catch(() => {
        // Ignore close errors
      });
      this.audioContext = null;
    }

    this.destination = null;
    this.masterGain = null;
    this.outputStream = null;

    this.removeAllListeners();
  }
}
