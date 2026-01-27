/**
 * StreamCrafter V2
 * Vanilla JS API with Phase 2 features:
 * - Multi-source support
 * - Audio mixing
 * - Quality switching
 * - Auto-reconnection
 */

import { IngestControllerV2 } from "../core/IngestControllerV2";
import type {
  IngestControllerConfigV2,
  IngestState,
  IngestStateContextV2,
  IngestStats,
  CaptureOptions,
  ScreenCaptureOptions,
  DeviceInfo,
  MediaSource,
  QualityProfile,
  SourceAddedEvent,
  SourceRemovedEvent,
  SourceUpdatedEvent,
} from "../types";

export interface StreamCrafterV2Config extends IngestControllerConfigV2 {}

export class StreamCrafterV2 {
  private controller: IngestControllerV2;

  constructor(config: StreamCrafterV2Config) {
    this.controller = new IngestControllerV2(config);
  }

  // ========================================
  // Event Handling
  // ========================================

  /**
   * Subscribe to state changes
   */
  onStateChange(
    handler: (event: { state: IngestState; context?: IngestStateContextV2 }) => void
  ): () => void {
    return this.controller.on("stateChange", handler);
  }

  /**
   * Subscribe to stats updates
   */
  onStatsUpdate(handler: (stats: IngestStats) => void): () => void {
    return this.controller.on("statsUpdate", handler);
  }

  /**
   * Subscribe to device changes
   */
  onDeviceChange(handler: (event: { devices: DeviceInfo[] }) => void): () => void {
    return this.controller.on("deviceChange", handler);
  }

  /**
   * Subscribe to errors
   */
  onError(handler: (event: { error: string; recoverable: boolean }) => void): () => void {
    return this.controller.on("error", handler);
  }

  /**
   * Subscribe to source added events
   */
  onSourceAdded(handler: (event: SourceAddedEvent) => void): () => void {
    return this.controller.on("sourceAdded", handler);
  }

  /**
   * Subscribe to source removed events
   */
  onSourceRemoved(handler: (event: SourceRemovedEvent) => void): () => void {
    return this.controller.on("sourceRemoved", handler);
  }

  /**
   * Subscribe to source updated events
   */
  onSourceUpdated(handler: (event: SourceUpdatedEvent) => void): () => void {
    return this.controller.on("sourceUpdated", handler);
  }

  /**
   * Subscribe to quality changed events
   */
  onQualityChanged(
    handler: (event: { profile: QualityProfile; previousProfile: QualityProfile }) => void
  ): () => void {
    return this.controller.on("qualityChanged", handler);
  }

  /**
   * Subscribe to reconnection attempt events
   */
  onReconnectionAttempt(
    handler: (event: { attempt: number; maxAttempts: number }) => void
  ): () => void {
    return this.controller.on("reconnectionAttempt", handler);
  }

  /**
   * Subscribe to reconnection success events
   */
  onReconnectionSuccess(handler: () => void): () => void {
    return this.controller.on("reconnectionSuccess", () => handler());
  }

  /**
   * Subscribe to reconnection failed events
   */
  onReconnectionFailed(handler: (event: { error: string }) => void): () => void {
    return this.controller.on("reconnectionFailed", handler);
  }

  // ========================================
  // Capture Methods
  // ========================================

  /**
   * Start camera/mic capture
   */
  async startCamera(options?: CaptureOptions): Promise<MediaSource> {
    return this.controller.startCamera(options);
  }

  /**
   * Start screen share capture
   */
  async startScreenShare(options?: ScreenCaptureOptions): Promise<MediaSource | null> {
    return this.controller.startScreenShare(options);
  }

  /**
   * Add a custom media source
   */
  addCustomSource(stream: MediaStream, label: string): MediaSource {
    return this.controller.addCustomSource(stream, label);
  }

  /**
   * Remove a source by ID
   */
  removeSource(sourceId: string): void {
    this.controller.removeSource(sourceId);
  }

  /**
   * Stop all capture
   */
  async stopCapture(): Promise<void> {
    return this.controller.stopCapture();
  }

  // ========================================
  // Source Management
  // ========================================

  /**
   * Get all sources
   */
  getSources(): MediaSource[] {
    return this.controller.getSources();
  }

  /**
   * Get a specific source by ID
   */
  getSource(id: string): MediaSource | undefined {
    return this.controller.getSource(id);
  }

  /**
   * Set source volume (0.0 - 1.0)
   */
  setSourceVolume(sourceId: string, volume: number): void {
    this.controller.setSourceVolume(sourceId, volume);
  }

  /**
   * Mute/unmute a source
   */
  setSourceMuted(sourceId: string, muted: boolean): void {
    this.controller.setSourceMuted(sourceId, muted);
  }

  /**
   * Set source active state
   */
  setSourceActive(sourceId: string, active: boolean): void {
    this.controller.setSourceActive(sourceId, active);
  }

  // ========================================
  // Streaming Methods
  // ========================================

  /**
   * Start streaming to WHIP endpoint
   */
  async startStreaming(): Promise<void> {
    return this.controller.startStreaming();
  }

  /**
   * Stop streaming
   */
  async stopStreaming(): Promise<void> {
    return this.controller.stopStreaming();
  }

  // ========================================
  // Quality Methods
  // ========================================

  /**
   * Get current quality profile
   */
  getQualityProfile(): QualityProfile {
    return this.controller.getQualityProfile();
  }

  /**
   * Set quality profile
   */
  async setQualityProfile(profile: QualityProfile): Promise<void> {
    return this.controller.setQualityProfile(profile);
  }

  // ========================================
  // Device Methods
  // ========================================

  /**
   * Get available devices
   */
  async getDevices(): Promise<DeviceInfo[]> {
    return this.controller.getDevices();
  }

  /**
   * Switch video device while streaming
   */
  async switchVideoDevice(deviceId: string): Promise<void> {
    return this.controller.switchVideoDevice(deviceId);
  }

  /**
   * Switch audio device while streaming
   */
  async switchAudioDevice(deviceId: string): Promise<void> {
    return this.controller.switchAudioDevice(deviceId);
  }

  // ========================================
  // State Methods
  // ========================================

  /**
   * Get current state
   */
  getState(): IngestState {
    return this.controller.getState();
  }

  /**
   * Get state context
   */
  getStateContext(): IngestStateContextV2 {
    return this.controller.getStateContext();
  }

  /**
   * Get current media stream (combined output)
   */
  getMediaStream(): MediaStream | null {
    return this.controller.getMediaStream();
  }

  /**
   * Get current stats
   */
  async getStats(): Promise<IngestStats | null> {
    return this.controller.getStats();
  }

  /**
   * Check if streaming
   */
  isStreaming(): boolean {
    return this.controller.isStreaming();
  }

  /**
   * Check if capturing
   */
  isCapturing(): boolean {
    return this.controller.isCapturing();
  }

  /**
   * Check if reconnecting
   */
  isReconnecting(): boolean {
    return this.controller.isReconnecting();
  }

  // ========================================
  // Advanced Access
  // ========================================

  /**
   * Get the audio mixer for direct control
   */
  getAudioMixer() {
    return this.controller.getAudioMixer();
  }

  /**
   * Get the reconnection manager for direct control
   */
  getReconnectionManager() {
    return this.controller.getReconnectionManager();
  }

  // ========================================
  // Lifecycle
  // ========================================

  /**
   * Destroy the instance
   */
  destroy(): void {
    this.controller.destroy();
  }
}
