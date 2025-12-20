/**
 * Screen Capture
 * Wrapper for getDisplayMedia with proper error handling
 * Supports multiple simultaneous screen captures
 */

import { TypedEventEmitter } from './EventEmitter';
import type { ScreenCaptureOptions } from '../types';

interface ScreenCaptureEvents {
  started: { stream: MediaStream; captureId: string };
  ended: { captureId: string; stream: MediaStream | null; reason: string };
  error: { message: string; error?: Error };
}

interface CaptureInfo {
  stream: MediaStream;
  label: string;
}

export class ScreenCapture extends TypedEventEmitter<ScreenCaptureEvents> {
  private captures: Map<string, CaptureInfo> = new Map();
  private captureCounter = 0;

  /**
   * Start a new screen capture
   * Each call creates a new capture - supports multiple simultaneous captures
   */
  async start(options: ScreenCaptureOptions = {}): Promise<MediaStream | null> {
    try {
      // Build video constraints with higher framerate for smooth screen share
      let videoConstraints: MediaTrackConstraints | boolean;

      if (options.video === false) {
        videoConstraints = false;
      } else if (typeof options.video === 'object') {
        // Use custom video constraints if provided as object, merged with defaults
        const customVideo = options.video as MediaTrackConstraints;
        videoConstraints = {
          // Default values
          frameRate: { ideal: 30, max: 60 },
          width: { ideal: 1920 },
          height: { ideal: 1080 },
          // Merge custom constraints (overrides defaults)
          ...customVideo,
          // Always add cursor if specified
          ...(options.cursor !== undefined ? { cursor: options.cursor } : {}),
        };
      } else {
        // Default constraints
        videoConstraints = {
          // Request high framerate for smooth screen capture
          frameRate: { ideal: 30, max: 60 },
          // Request good resolution - let browser pick best for display
          width: { ideal: 1920 },
          height: { ideal: 1080 },
          // Prefer motion over sharpness when encoding is under pressure
          ...(options.cursor !== undefined ? { cursor: options.cursor } : {}),
        };
      }

      const constraints: DisplayMediaStreamOptions = {
        video: videoConstraints,
        audio: options.audio ?? false,
      };

      // Add preferCurrentTab if supported and requested
      if (options.preferCurrentTab && 'preferCurrentTab' in constraints) {
        (constraints as Record<string, unknown>).preferCurrentTab = true;
      }

      // Add surfaceSwitching if requested (Chrome 107+)
      if (options.surfaceSwitching) {
        (constraints as Record<string, unknown>).surfaceSwitching = 'include';
      }

      // Add selfBrowserSurface if requested (Chrome 107+)
      if (options.selfBrowserSurface !== undefined) {
        (constraints as Record<string, unknown>).selfBrowserSurface = options.selfBrowserSurface;
      }

      // Add monitorTypeSurfaces if requested (Chrome 119+)
      if (options.monitorTypeSurfaces) {
        (constraints as Record<string, unknown>).monitorTypeSurfaces = options.monitorTypeSurfaces;
      }

      // Add system audio preference if supported
      if (options.systemAudio && constraints.audio) {
        if (typeof constraints.audio === 'object') {
          (constraints.audio as Record<string, unknown>).systemAudio = options.systemAudio;
        }
      }

      const stream = await navigator.mediaDevices.getDisplayMedia(constraints);

      // Generate a unique ID for this capture
      const captureId = `screen-${++this.captureCounter}-${Date.now()}`;

      // Get a label for this capture from the video track
      const videoTrack = stream.getVideoTracks()[0];
      const label = videoTrack?.label || `Screen ${this.captureCounter}`;

      // Store the capture
      this.captures.set(captureId, { stream, label });

      // Listen for track ended events (user stopped sharing)
      stream.getTracks().forEach((track) => {
        track.addEventListener('ended', () => {
          this.handleTrackEnded(captureId, stream);
        });
      });

      this.emit('started', { stream, captureId });

      return stream;
    } catch (error) {
      const err = error instanceof Error ? error : new Error(String(error));

      // User cancelled is not an error
      if (err.name === 'AbortError' || err.name === 'NotAllowedError') {
        this.emit('ended', { captureId: '', stream: null, reason: 'cancelled' });
        return null;
      }

      this.emit('error', {
        message: `Screen capture failed: ${err.message}`,
        error: err,
      });

      throw error;
    }
  }

  /**
   * Handle track ended event
   */
  private handleTrackEnded(captureId: string, stream: MediaStream): void {
    // Check if all tracks have ended
    const activeTracks = stream.getTracks().filter((t) => t.readyState === 'live');
    if (activeTracks.length === 0) {
      this.captures.delete(captureId);
      this.emit('ended', { captureId, stream, reason: 'user_stopped' });
    }
  }

  /**
   * Stop a specific screen capture by its stream
   */
  stopByStream(stream: MediaStream): void {
    for (const [captureId, info] of this.captures) {
      if (info.stream === stream) {
        info.stream.getTracks().forEach((track) => track.stop());
        this.captures.delete(captureId);
        this.emit('ended', { captureId, stream, reason: 'stopped' });
        return;
      }
    }
  }

  /**
   * Stop all screen captures
   */
  stop(): void {
    for (const [captureId, info] of this.captures) {
      info.stream.getTracks().forEach((track) => track.stop());
      this.emit('ended', { captureId, stream: info.stream, reason: 'stopped' });
    }
    this.captures.clear();
  }

  /**
   * Get all active captures
   */
  getCaptures(): Array<{ captureId: string; stream: MediaStream; label: string }> {
    return Array.from(this.captures.entries()).map(([captureId, info]) => ({
      captureId,
      stream: info.stream,
      label: info.label,
    }));
  }

  /**
   * Check if any screen capture is active
   */
  isActive(): boolean {
    return this.captures.size > 0;
  }

  /**
   * Get the count of active captures
   */
  getCaptureCount(): number {
    return this.captures.size;
  }

  // Legacy methods for backwards compatibility (use first capture)

  /**
   * Get current stream (first capture for backwards compatibility)
   * @deprecated Use getCaptures() instead
   */
  getStream(): MediaStream | null {
    const first = this.captures.values().next().value;
    return first?.stream ?? null;
  }

  /**
   * Get video track (first capture for backwards compatibility)
   * @deprecated Use getCaptures() instead
   */
  getVideoTrack(): MediaStreamTrack | null {
    const stream = this.getStream();
    return stream?.getVideoTracks()[0] ?? null;
  }

  /**
   * Get audio track (first capture for backwards compatibility)
   * @deprecated Use getCaptures() instead
   */
  getAudioTrack(): MediaStreamTrack | null {
    const stream = this.getStream();
    return stream?.getAudioTracks()[0] ?? null;
  }

  /**
   * Check if system audio is being captured
   */
  hasAudio(): boolean {
    for (const [, info] of this.captures) {
      if (info.stream.getAudioTracks().length > 0) {
        return true;
      }
    }
    return false;
  }

  /**
   * Clean up resources
   */
  destroy(): void {
    this.stop();
    this.removeAllListeners();
  }
}
