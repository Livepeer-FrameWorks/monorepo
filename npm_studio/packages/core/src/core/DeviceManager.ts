/**
 * Device Manager
 * Handles camera/microphone enumeration and capture
 */

import { TypedEventEmitter } from "./EventEmitter";
import { buildMediaConstraints } from "./MediaConstraints";
import type { DeviceInfo, CaptureOptions, QualityProfile } from "../types";

interface DeviceManagerEvents {
  devicesChanged: { devices: DeviceInfo[] };
  permissionChanged: { granted: boolean; denied: boolean };
  error: { message: string; error?: Error };
}

export class DeviceManager extends TypedEventEmitter<DeviceManagerEvents> {
  private devices: DeviceInfo[] = [];
  private currentStream: MediaStream | null = null;
  private destroyed = false;
  private releasedStreams = new WeakSet<MediaStream>();
  private videoReplacements = new WeakMap<MediaStream, symbol>();
  private audioReplacements = new WeakMap<MediaStream, symbol>();
  private mediaDevices: MediaDevices | undefined;
  private readonly handleDeviceChange = async (): Promise<void> => {
    try {
      const devices = await this.enumerateDevices();
      if (!this.destroyed) this.emit("devicesChanged", { devices });
    } catch (error) {
      if (!this.destroyed) {
        this.emit("error", {
          message: "Device enumeration failed",
          error: error instanceof Error ? error : new Error(String(error)),
        });
      }
    }
  };
  private permissionStatus: { video: boolean; audio: boolean } = {
    video: false,
    audio: false,
  };

  constructor() {
    super();
    this.setupDeviceChangeListener();
  }

  /**
   * Set up listener for device changes
   */
  private setupDeviceChangeListener(): void {
    if (typeof navigator !== "undefined" && navigator.mediaDevices) {
      this.mediaDevices = navigator.mediaDevices;
      this.mediaDevices.addEventListener("devicechange", this.handleDeviceChange);
    }
  }

  private assertActive(): void {
    if (this.destroyed) throw new DOMException("Device manager destroyed", "AbortError");
  }

  private async capture(constraints: MediaStreamConstraints): Promise<MediaStream> {
    this.assertActive();
    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getUserMedia(constraints);
    } catch (error) {
      this.assertActive();
      throw error;
    }
    // Permission prompts cannot be cancelled; their late tracks still need an owner.
    if (this.destroyed) {
      stream.getTracks().forEach((track) => track.stop());
      this.assertActive();
    }
    return stream;
  }

  /**
   * Enumerate all media devices
   */
  async enumerateDevices(): Promise<DeviceInfo[]> {
    if (this.destroyed) return [];
    if (!navigator.mediaDevices?.enumerateDevices) {
      throw new Error("enumerateDevices not supported");
    }

    const devices = await navigator.mediaDevices.enumerateDevices();
    if (this.destroyed) return [];

    this.devices = devices
      .filter((d) => d.kind === "audioinput" || d.kind === "videoinput" || d.kind === "audiooutput")
      .map((d) => ({
        deviceId: d.deviceId,
        kind: d.kind as DeviceInfo["kind"],
        label: d.label || `${d.kind} (${d.deviceId.slice(0, 8)}...)`,
        groupId: d.groupId,
      }));

    return this.devices;
  }

  /**
   * Get video input devices (cameras)
   */
  async getVideoInputs(): Promise<DeviceInfo[]> {
    await this.enumerateDevices();
    return this.devices.filter((d) => d.kind === "videoinput");
  }

  /**
   * Get audio input devices (microphones)
   */
  async getAudioInputs(): Promise<DeviceInfo[]> {
    await this.enumerateDevices();
    return this.devices.filter((d) => d.kind === "audioinput");
  }

  /**
   * Get audio output devices (speakers)
   */
  async getAudioOutputs(): Promise<DeviceInfo[]> {
    await this.enumerateDevices();
    return this.devices.filter((d) => d.kind === "audiooutput");
  }

  /**
   * Request permissions for camera and/or microphone
   */
  async requestPermissions(
    options: { video?: boolean; audio?: boolean } = { video: true, audio: true }
  ): Promise<{ video: boolean; audio: boolean }> {
    try {
      // Request a temporary stream to trigger permission prompts
      const stream = await this.capture({
        video: options.video,
        audio: options.audio,
      });

      // Stop all tracks immediately
      stream.getTracks().forEach((track) => track.stop());

      // Update permission status
      if (options.video) this.permissionStatus.video = true;
      if (options.audio) this.permissionStatus.audio = true;

      // Re-enumerate devices to get labels
      await this.enumerateDevices();
      this.assertActive();

      this.emit("permissionChanged", {
        granted: true,
        denied: false,
      });

      return this.permissionStatus;
    } catch (error) {
      this.assertActive();
      const err = error instanceof Error ? error : new Error(String(error));

      if (err.name === "NotAllowedError" || err.name === "PermissionDeniedError") {
        this.emit("permissionChanged", {
          granted: false,
          denied: true,
        });
      }

      this.emit("error", {
        message: `Permission request failed: ${err.message}`,
        error: err,
      });

      throw error;
    }
  }

  /**
   * Check if permissions are granted
   */
  hasPermission(kind: "video" | "audio"): boolean {
    return this.permissionStatus[kind];
  }

  /**
   * Get user media with quality profile
   */
  async getUserMedia(options: CaptureOptions = {}): Promise<MediaStream> {
    const profile: QualityProfile = options.profile || "professional";

    let constraints: MediaStreamConstraints;

    if (options.customConstraints) {
      constraints = options.customConstraints;
    } else {
      constraints = buildMediaConstraints(profile, {
        videoDeviceId: options.videoDeviceId,
        audioDeviceId: options.audioDeviceId,
        facingMode: options.facingMode,
      });
    }
    if (options.audio === false) {
      constraints = { ...constraints, audio: false };
    }

    try {
      // Try with all constraints first
      const stream = await this.capture(constraints);
      this.currentStream = stream;

      // Update permission status
      if (stream.getVideoTracks().length > 0) this.permissionStatus.video = true;
      if (stream.getAudioTracks().length > 0) this.permissionStatus.audio = true;

      return stream;
    } catch (error) {
      this.assertActive();
      // Fallback: try without specific device constraints
      const err = error instanceof Error ? error : new Error(String(error));

      if (err.name === "OverconstrainedError") {
        // Relax the size and frame rate; a camera the broadcaster picked stays picked.
        const relaxedConstraints: MediaStreamConstraints = {
          video: options.videoDeviceId
            ? { deviceId: { exact: options.videoDeviceId } }
            : constraints.video
              ? true
              : false,
          audio: constraints.audio ? true : false,
        };

        const stream = await this.capture(relaxedConstraints);
        this.currentStream = stream;
        return stream;
      }

      this.emit("error", {
        message: `getUserMedia failed: ${err.message}`,
        error: err,
      });

      throw error;
    }
  }

  /**
   * Get current stream
   */
  getStream(): MediaStream | null {
    return this.currentStream;
  }

  /**
   * Forget a stream whose capture has ended, so nothing reopens a device into it.
   */
  releaseStream(stream: MediaStream): void {
    this.releasedStreams.add(stream);
    this.videoReplacements.delete(stream);
    if (this.currentStream === stream) {
      this.currentStream = null;
    }
  }

  /**
   * Stop all tracks on current stream
   */
  stopAllTracks(): void {
    if (this.currentStream) {
      this.releasedStreams.add(this.currentStream);
      this.videoReplacements.delete(this.currentStream);
      this.currentStream.getTracks().forEach((track) => {
        track.stop();
      });
      this.currentStream = null;
    }
  }

  /**
   * Replace the video track of a stream, by default the last one captured
   */
  async replaceVideoTrack(
    deviceId: string,
    profile: QualityProfile = "professional",
    stream: MediaStream | null = this.currentStream
  ): Promise<MediaStreamTrack | null> {
    this.assertActive();
    if (!stream) {
      throw new Error("No active stream to replace track in");
    }

    if (this.releasedStreams.has(stream)) {
      throw new DOMException("Capture stream released", "AbortError");
    }
    const replacement = Symbol();
    this.videoReplacements.set(stream, replacement);

    // Stop current video track
    const currentVideoTrack = stream.getVideoTracks()[0];
    if (currentVideoTrack) {
      currentVideoTrack.stop();
      stream.removeTrack(currentVideoTrack);
    }

    // Get new video track
    const constraints = buildMediaConstraints(profile, { videoDeviceId: deviceId });
    const newStream = await this.capture({
      video: constraints.video,
      audio: false,
    });

    if (this.releasedStreams.has(stream) || this.videoReplacements.get(stream) !== replacement) {
      newStream.getTracks().forEach((track) => track.stop());
      throw new DOMException("Capture stream released or superseded", "AbortError");
    }

    const newTrack = newStream.getVideoTracks()[0];
    if (newTrack) {
      stream.addTrack(newTrack);
    }

    return newTrack || null;
  }

  /**
   * Replace audio track in current stream
   */
  async replaceAudioTrack(
    deviceId: string,
    profile: QualityProfile = "professional"
  ): Promise<MediaStreamTrack | null> {
    this.assertActive();
    if (!this.currentStream) {
      throw new Error("No active stream to replace track in");
    }

    // Stop current audio track
    const targetStream = this.currentStream;
    const replacement = Symbol();
    this.audioReplacements.set(targetStream, replacement);
    const currentAudioTrack = targetStream.getAudioTracks()[0];
    if (currentAudioTrack) {
      currentAudioTrack.stop();
      targetStream.removeTrack(currentAudioTrack);
    }

    // Get new audio track
    const constraints = buildMediaConstraints(profile, { audioDeviceId: deviceId });
    const newStream = await this.capture({
      video: false,
      audio: constraints.audio,
    });

    if (
      this.currentStream !== targetStream ||
      this.releasedStreams.has(targetStream) ||
      this.audioReplacements.get(targetStream) !== replacement
    ) {
      newStream.getTracks().forEach((track) => track.stop());
      throw new DOMException("Capture stream released or superseded", "AbortError");
    }

    const newTrack = newStream.getAudioTracks()[0];
    if (newTrack) {
      targetStream.addTrack(newTrack);
    }

    return newTrack || null;
  }

  /**
   * Open a microphone on its own: the given device, or the default one when
   * that device is no longer there.
   */
  async openMicrophone(
    deviceId: string | undefined,
    profile: QualityProfile = "professional"
  ): Promise<MediaStreamTrack | null> {
    const open = (audioDeviceId?: string) =>
      this.capture({
        video: false,
        audio: buildMediaConstraints(profile, { audioDeviceId }).audio,
      });

    const stream = await open(deviceId).catch((error: unknown) => {
      const name = error instanceof Error ? error.name : "";
      if (deviceId && (name === "OverconstrainedError" || name === "NotFoundError")) {
        return open();
      }
      throw error;
    });

    return stream.getAudioTracks()[0] ?? null;
  }

  /**
   * Get all cached devices
   */
  getAllDevices(): DeviceInfo[] {
    return [...this.devices];
  }

  /**
   * Clean up resources
   */
  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.mediaDevices?.removeEventListener("devicechange", this.handleDeviceChange);
    this.stopAllTracks();
    this.devices = [];
    this.removeAllListeners();
  }
}
