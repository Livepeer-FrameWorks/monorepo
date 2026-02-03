/**
 * WHIP Client
 * WebRTC-HTTP Ingestion Protocol client for streaming
 * Ported from StreamCrafter useWhipStreaming.js
 */

import { TypedEventEmitter } from "./EventEmitter";
import type { WhipClientConfig, WhipConnectionState, WhipClientEvents } from "../types";
import type {
  EncoderManager,
  EncodedVideoChunkData,
  EncodedAudioChunkData,
} from "./EncoderManager";

export class WhipClient extends TypedEventEmitter<WhipClientEvents> {
  private config: WhipClientConfig;
  private peerConnection: RTCPeerConnection | null = null;
  private videoTrackGenerator: MediaStreamTrackGenerator | null = null;
  private audioTrackGenerator: MediaStreamTrackGenerator | null = null;
  private state: WhipConnectionState = "disconnected";

  // Writer management to prevent locking issues
  private videoWriter: WritableStreamDefaultWriter<VideoFrame> | null = null;
  private audioWriter: WritableStreamDefaultWriter<AudioData> | null = null;
  private videoWriteQueue: Array<{
    frame: VideoFrame;
    resolve: (value: boolean) => void;
    reject: (error: Error) => void;
  }> = [];
  private audioWriteQueue: Array<{
    audioData: AudioData;
    resolve: (value: boolean) => void;
    reject: (error: Error) => void;
  }> = [];
  private isProcessingVideoQueue = false;
  private isProcessingAudioQueue = false;

  // RTCRtpScriptTransform workers for WebCodecs integration
  private videoTransformWorker: Worker | null = null;
  private audioTransformWorker: Worker | null = null;
  private encoderListenerCleanup: (() => void) | null = null;

  // Negotiated codecs (populated after connection)
  private negotiatedVideoCodec: string | null = null;
  private negotiatedAudioCodec: string | null = null;

  // WHIP resource URL returned in Location header (for DELETE on disconnect)
  private resourceUrl: string | null = null;

  constructor(config: WhipClientConfig) {
    super();
    this.config = config;
  }

  /**
   * Debug logging helper
   */
  private log(message: string, data?: unknown): void {
    if (this.config.debug) {
      console.log(`[WHIP] ${message}`, data ?? "");
    }
  }

  /**
   * Error logging helper
   */
  private logError(message: string, error?: Error): void {
    console.error(`[WHIP ERROR] ${message}`, error ?? "");
    this.emit("error", { message, error });
  }

  /**
   * Update connection state
   */
  private setState(newState: WhipConnectionState): void {
    const previousState = this.state;
    this.state = newState;
    this.emit("stateChange", { state: newState, previousState });
  }

  // ==========================================================================
  // Codec alignment for WebCodecs path
  // ==========================================================================

  /**
   * Set codec preferences on transceivers before creating offer.
   * Prefers VP9 (royalty-free, better compression) then H264 for video.
   * Prefers Opus for audio.
   */
  private preferCodecs(pc: RTCPeerConnection): void {
    const transceivers = pc.getTransceivers();

    for (const transceiver of transceivers) {
      if (!transceiver.setCodecPreferences) {
        continue;
      }

      const trackKind = transceiver.sender.track?.kind;

      if (trackKind === "video") {
        const capabilities = RTCRtpSender.getCapabilities("video");
        if (!capabilities?.codecs) continue;

        // Filter to VP9 and H264, preferring VP9
        const preferred = capabilities.codecs
          .filter((c) => c.mimeType === "video/VP9" || c.mimeType === "video/H264")
          .sort((a, b) => {
            // VP9 first, then H264
            if (a.mimeType === "video/VP9" && b.mimeType !== "video/VP9") return -1;
            if (a.mimeType !== "video/VP9" && b.mimeType === "video/VP9") return 1;
            return 0;
          });

        if (preferred.length > 0) {
          try {
            transceiver.setCodecPreferences(preferred);
            this.log(
              "Set video codec preferences",
              preferred.map((c) => c.mimeType)
            );
          } catch (err) {
            this.log("Failed to set video codec preferences", err);
          }
        }
      }

      if (trackKind === "audio") {
        const capabilities = RTCRtpSender.getCapabilities("audio");
        if (!capabilities?.codecs) continue;

        // Filter to Opus only
        const preferred = capabilities.codecs.filter((c) => c.mimeType === "audio/opus");

        if (preferred.length > 0) {
          try {
            transceiver.setCodecPreferences(preferred);
            this.log(
              "Set audio codec preferences",
              preferred.map((c) => c.mimeType)
            );
          } catch (err) {
            this.log("Failed to set audio codec preferences", err);
          }
        }
      }
    }
  }

  /**
   * Verify negotiated codecs after connection.
   * Populates negotiatedVideoCodec and negotiatedAudioCodec.
   */
  private verifyCodecAlignment(): void {
    if (!this.peerConnection) return;

    const senders = this.peerConnection.getSenders();

    for (const sender of senders) {
      const params = sender.getParameters();
      const codec = params.codecs?.[0];

      if (sender.track?.kind === "video" && codec?.mimeType) {
        this.negotiatedVideoCodec = codec.mimeType;
        this.log("Negotiated video codec", codec.mimeType);
      }

      if (sender.track?.kind === "audio" && codec?.mimeType) {
        this.negotiatedAudioCodec = codec.mimeType;
        this.log("Negotiated audio codec", codec.mimeType);
      }
    }
  }

  /**
   * Check if encoded frame insertion is safe.
   * Returns true if negotiated codecs are compatible with WebCodecs output.
   *
   * For WebCodecs insertion to work correctly:
   * - Video must be VP9 or H264 (what WebCodecs can produce)
   * - Audio must be Opus (what WebCodecs can produce)
   */
  canUseEncodedInsertion(): boolean {
    // Must have a connection
    if (!this.peerConnection || this.state !== "connected") {
      this.log("canUseEncodedInsertion: no connection", {
        hasPC: !!this.peerConnection,
        state: this.state,
      });
      return false;
    }

    // Must have RTCRtpScriptTransform support
    if (typeof RTCRtpScriptTransform === "undefined") {
      this.log("canUseEncodedInsertion: RTCRtpScriptTransform not supported");
      return false;
    }

    // Must have senders with transform support
    const senders = this.peerConnection.getSenders();
    const hasTransformSupport = senders.some((s) => "transform" in s);
    if (!hasTransformSupport) {
      this.log("Sender transform not supported");
      return false;
    }

    // Check video codec alignment
    if (this.negotiatedVideoCodec) {
      const videoOk =
        this.negotiatedVideoCodec === "video/VP9" || this.negotiatedVideoCodec === "video/H264";

      if (!videoOk) {
        this.log("Video codec not compatible with WebCodecs", this.negotiatedVideoCodec);
        return false;
      }
    }

    // Check audio codec alignment
    if (this.negotiatedAudioCodec) {
      const audioOk = this.negotiatedAudioCodec === "audio/opus";

      if (!audioOk) {
        this.log("Audio codec not compatible with WebCodecs", this.negotiatedAudioCodec);
        return false;
      }
    }

    return true;
  }

  /**
   * Get the negotiated video codec MIME type.
   */
  getNegotiatedVideoCodec(): string | null {
    return this.negotiatedVideoCodec;
  }

  /**
   * Get the negotiated audio codec MIME type.
   */
  getNegotiatedAudioCodec(): string | null {
    return this.negotiatedAudioCodec;
  }

  /**
   * Check if connected.
   */
  get isConnected(): boolean {
    return this.state === "connected";
  }

  /**
   * Connect to WHIP endpoint with a MediaStream
   */
  async connect(stream: MediaStream): Promise<void> {
    try {
      this.log("Starting WHIP connection");
      this.setState("connecting");

      if (!this.config.whipUrl) {
        throw new Error("WHIP URL is required");
      }

      // Create RTCPeerConnection
      const pcConfig: RTCConfiguration = {
        iceServers: this.config.iceServers || [],
      };

      this.log("Creating RTCPeerConnection", pcConfig);
      const pc = new RTCPeerConnection(pcConfig);

      // Set up connection state monitoring
      pc.onconnectionstatechange = () => {
        const state = pc.connectionState;
        this.log(`Connection state changed: ${state}`);

        switch (state) {
          case "connected":
            this.log("WHIP streaming connected successfully");
            this.setState("connected");
            break;
          case "disconnected":
            this.setState("disconnected");
            break;
          case "failed":
            this.setState("failed");
            break;
          case "closed":
            this.setState("closed");
            break;
        }
      };

      pc.oniceconnectionstatechange = () => {
        this.log(`ICE connection state: ${pc.iceConnectionState}`);
      };

      pc.onicegatheringstatechange = () => {
        this.log(`ICE gathering state: ${pc.iceGatheringState}`);
      };

      pc.onicecandidate = (event) => {
        this.emit("iceCandidate", { candidate: event.candidate });
        if (event.candidate) {
          this.log("ICE candidate generated", event.candidate.candidate);
        } else {
          this.log("ICE candidate gathering complete");
        }
      };

      // Add tracks from the stream
      this.log("Adding tracks to peer connection");
      stream.getTracks().forEach((track, index) => {
        this.log(`Adding ${track.kind} track ${index}`, {
          id: track.id,
          kind: track.kind,
          enabled: track.enabled,
          readyState: track.readyState,
        });
        pc.addTrack(track, stream);
      });

      this.peerConnection = pc;

      // Set codec preferences before creating offer (for WebCodecs alignment)
      this.preferCodecs(pc);

      // Create and set local description
      this.log("Creating offer");
      const offer = await pc.createOffer({
        offerToReceiveAudio: false,
        offerToReceiveVideo: false,
      });

      this.log("Setting local description");
      await pc.setLocalDescription(offer);

      this.log("Local SDP offer created", {
        type: offer.type,
        sdpLength: offer.sdp?.length,
      });

      // Send offer to WHIP endpoint
      this.log(`Sending offer to WHIP endpoint: ${this.config.whipUrl}`);

      const response = await fetch(this.config.whipUrl, {
        method: "POST",
        headers: {
          "Content-Type": "application/sdp",
          Accept: "application/sdp",
        },
        body: offer.sdp,
      });

      this.log(`WHIP response status: ${response.status} ${response.statusText}`);

      if (!response.ok) {
        const errorText = await response.text().catch(() => "Unknown error");
        throw new Error(
          `WHIP request failed: ${response.status} ${response.statusText} - ${errorText}`
        );
      }

      // Store WHIP resource URL from Location header (per RFC 9725)
      // This URL is used for DELETE on disconnect
      this.resourceUrl = response.headers.get("Location");
      if (this.resourceUrl) {
        this.log("WHIP resource URL:", this.resourceUrl);
      }

      // Get and set remote description
      const answerSdp = await response.text();
      this.log("Received SDP answer", {
        length: answerSdp.length,
      });

      await pc.setRemoteDescription({
        type: "answer",
        sdp: answerSdp,
      });

      this.log("Remote description set successfully");

      // Verify negotiated codecs for WebCodecs alignment
      this.verifyCodecAlignment();

      this.log("WHIP connection established, waiting for ICE connection...");
    } catch (error) {
      this.logError("Failed to connect", error instanceof Error ? error : new Error(String(error)));
      this.setState("failed");
      this.cleanup();
      throw error;
    }
  }

  /**
   * Connect using MediaStreamTrackGenerators (for WebCodecs path)
   */
  async connectWithGenerators(): Promise<{
    videoGenerator: MediaStreamTrackGenerator;
    audioGenerator: MediaStreamTrackGenerator;
  }> {
    try {
      this.log("Starting WHIP connection with track generators");
      this.setState("connecting");

      if (!this.config.whipUrl) {
        throw new Error("WHIP URL is required");
      }

      // Create track generators
      this.log("Creating MediaStreamTrackGenerators");
      const videoGenerator = new MediaStreamTrackGenerator({ kind: "video" });
      const audioGenerator = new MediaStreamTrackGenerator({ kind: "audio" });

      this.videoTrackGenerator = videoGenerator;
      this.audioTrackGenerator = audioGenerator;

      this.log("Track generators created successfully");

      // Create RTCPeerConnection
      const pcConfig: RTCConfiguration = {
        iceServers: this.config.iceServers || [],
      };

      this.log("Creating RTCPeerConnection", pcConfig);
      const pc = new RTCPeerConnection(pcConfig);

      // Set up connection state monitoring
      pc.onconnectionstatechange = () => {
        const state = pc.connectionState;
        this.log(`Connection state changed: ${state}`);

        switch (state) {
          case "connected":
            this.log("WHIP streaming connected successfully");
            this.setState("connected");
            break;
          case "disconnected":
            this.setState("disconnected");
            break;
          case "failed":
            this.setState("failed");
            break;
          case "closed":
            this.setState("closed");
            break;
        }
      };

      pc.oniceconnectionstatechange = () => {
        this.log(`ICE connection state: ${pc.iceConnectionState}`);
      };

      pc.onicegatheringstatechange = () => {
        this.log(`ICE gathering state: ${pc.iceGatheringState}`);
      };

      pc.onicecandidate = (event) => {
        this.emit("iceCandidate", { candidate: event.candidate });
        if (event.candidate) {
          this.log("ICE candidate generated", event.candidate.candidate);
        } else {
          this.log("ICE candidate gathering complete");
        }
      };

      // Add tracks to peer connection
      const mediaStream = new MediaStream([videoGenerator, audioGenerator]);

      this.log("Adding tracks to peer connection");
      mediaStream.getTracks().forEach((track, index) => {
        this.log(`Adding ${track.kind} track ${index}`, {
          id: track.id,
          kind: track.kind,
          enabled: track.enabled,
          readyState: track.readyState,
        });
        pc.addTrack(track, mediaStream);
      });

      this.peerConnection = pc;

      // Set codec preferences before creating offer
      this.preferCodecs(pc);

      // Create and set local description
      this.log("Creating offer");
      const offer = await pc.createOffer({
        offerToReceiveAudio: false,
        offerToReceiveVideo: false,
      });

      this.log("Setting local description");
      await pc.setLocalDescription(offer);

      // Send offer to WHIP endpoint
      this.log(`Sending offer to WHIP endpoint: ${this.config.whipUrl}`);

      const response = await fetch(this.config.whipUrl, {
        method: "POST",
        headers: {
          "Content-Type": "application/sdp",
          Accept: "application/sdp",
        },
        body: offer.sdp,
      });

      this.log(`WHIP response status: ${response.status} ${response.statusText}`);

      if (!response.ok) {
        const errorText = await response.text().catch(() => "Unknown error");
        throw new Error(
          `WHIP request failed: ${response.status} ${response.statusText} - ${errorText}`
        );
      }

      // Store WHIP resource URL from Location header (per RFC 9725)
      // This URL is used for DELETE on disconnect
      this.resourceUrl = response.headers.get("Location");
      if (this.resourceUrl) {
        this.log("WHIP resource URL:", this.resourceUrl);
      }

      // Get and set remote description
      const answerSdp = await response.text();
      this.log("Received SDP answer", { length: answerSdp.length });

      await pc.setRemoteDescription({
        type: "answer",
        sdp: answerSdp,
      });

      this.log("Remote description set successfully");

      // Verify negotiated codecs
      this.verifyCodecAlignment();

      this.log("WHIP connection established with generators");

      return { videoGenerator, audioGenerator };
    } catch (error) {
      this.logError(
        "Failed to connect with generators",
        error instanceof Error ? error : new Error(String(error))
      );
      this.setState("failed");
      this.cleanup();
      throw error;
    }
  }

  /**
   * Process video write queue
   */
  private async processVideoQueue(): Promise<void> {
    if (this.isProcessingVideoQueue || this.videoWriteQueue.length === 0) {
      return;
    }

    this.isProcessingVideoQueue = true;

    try {
      while (this.videoWriteQueue.length > 0) {
        const item = this.videoWriteQueue.shift();
        if (!item) continue;

        const { frame, resolve, reject } = item;

        if (!this.videoTrackGenerator) {
          reject(new Error("Video track generator not available"));
          continue;
        }

        try {
          if (!this.videoWriter) {
            this.videoWriter = this.videoTrackGenerator.writable.getWriter();
          }

          await this.videoWriter!.write(frame);
          resolve(true);
        } catch (error) {
          // Release writer on error and try to recreate
          if (this.videoWriter) {
            try {
              this.videoWriter.releaseLock();
            } catch {
              // Ignore release errors
            }
            this.videoWriter = null;
          }
          reject(error instanceof Error ? error : new Error(String(error)));
        }
      }
    } finally {
      this.isProcessingVideoQueue = false;
    }
  }

  /**
   * Process audio write queue
   */
  private async processAudioQueue(): Promise<void> {
    if (this.isProcessingAudioQueue || this.audioWriteQueue.length === 0) {
      return;
    }

    this.isProcessingAudioQueue = true;

    try {
      while (this.audioWriteQueue.length > 0) {
        const item = this.audioWriteQueue.shift();
        if (!item) continue;

        const { audioData, resolve, reject } = item;

        if (!this.audioTrackGenerator) {
          reject(new Error("Audio track generator not available"));
          continue;
        }

        try {
          if (!this.audioWriter) {
            this.audioWriter = this.audioTrackGenerator.writable.getWriter();
          }

          await this.audioWriter!.write(audioData);
          resolve(true);
        } catch (error) {
          // Release writer on error and try to recreate
          if (this.audioWriter) {
            try {
              this.audioWriter.releaseLock();
            } catch {
              // Ignore release errors
            }
            this.audioWriter = null;
          }
          reject(error instanceof Error ? error : new Error(String(error)));
        }
      }
    } finally {
      this.isProcessingAudioQueue = false;
    }
  }

  /**
   * Send video frame to WHIP stream (queued)
   */
  async sendVideoFrame(frame: VideoFrame): Promise<boolean> {
    if (!this.videoTrackGenerator) {
      if (frame) {
        try {
          frame.close();
        } catch {
          /* ignore */
        }
      }
      return false;
    }

    return new Promise((resolve, reject) => {
      this.videoWriteQueue.push({ frame, resolve, reject });
      this.processVideoQueue();
    });
  }

  /**
   * Send audio data to WHIP stream (queued)
   */
  async sendAudioData(audioData: AudioData): Promise<boolean> {
    if (!this.audioTrackGenerator) {
      if (audioData) {
        try {
          audioData.close();
        } catch {
          /* ignore */
        }
      }
      return false;
    }

    return new Promise((resolve, reject) => {
      this.audioWriteQueue.push({ audioData, resolve, reject });
      this.processAudioQueue();
    });
  }

  /**
   * Replace a track in the peer connection
   */
  async replaceTrack(oldTrack: MediaStreamTrack, newTrack: MediaStreamTrack): Promise<void> {
    if (!this.peerConnection) {
      throw new Error("No peer connection");
    }

    const sender = this.peerConnection.getSenders().find((s) => s.track?.kind === oldTrack.kind);

    if (!sender) {
      throw new Error(`No sender found for ${oldTrack.kind} track`);
    }

    await sender.replaceTrack(newTrack);
    this.log(`Replaced ${oldTrack.kind} track`);
  }

  /**
   * Add a track to the peer connection
   */
  async addTrack(track: MediaStreamTrack, stream?: MediaStream): Promise<void> {
    if (!this.peerConnection) {
      throw new Error("No peer connection");
    }

    this.peerConnection.addTrack(track, stream || new MediaStream([track]));
    this.log(`Added ${track.kind} track`);
  }

  /**
   * Get connection statistics
   */
  async getStats(): Promise<RTCStatsReport | null> {
    if (!this.peerConnection) {
      return null;
    }

    try {
      return await this.peerConnection.getStats();
    } catch (error) {
      this.logError(
        "Failed to get connection stats",
        error instanceof Error ? error : new Error(String(error))
      );
      return null;
    }
  }

  /**
   * Get current connection state
   */
  getState(): WhipConnectionState {
    return this.state;
  }

  /**
   * Get peer connection
   */
  getPeerConnection(): RTCPeerConnection | null {
    return this.peerConnection;
  }

  /**
   * Attach RTCRtpScriptTransform to inject WebCodecs-encoded chunks
   *
   * This method creates transform workers and attaches them to the RTP senders,
   * allowing us to replace browser-encoded frames with our WebCodecs-encoded chunks.
   *
   * @param encoderManager - The EncoderManager that produces encoded chunks
   * @param workerUrl - Optional URL to the rtcTransform worker (for bundled usage)
   */
  attachEncoderTransform(encoderManager: EncoderManager, workerUrl?: string): void {
    if (!this.peerConnection) {
      throw new Error("No peer connection - call connect() first");
    }

    // Check for RTCRtpScriptTransform support
    if (typeof RTCRtpScriptTransform === "undefined") {
      this.log("RTCRtpScriptTransform not supported, skipping encoder transform");
      return;
    }

    this.log("Attaching encoder transform");

    // Create transform workers using inlined worker bundle
    const createWorker = (): Worker | null => {
      // Custom worker URL takes precedence (for consumers who bundle separately)
      if (workerUrl) {
        return new Worker(workerUrl, { type: "module" });
      }

      // Preferred: load packaged worker relative to the built module URL.
      try {
        const packagedUrl = new URL("../workers/rtcTransform.worker.js", import.meta.url);
        return new Worker(packagedUrl, { type: "module" });
      } catch (err) {
        this.log("Packaged worker URL failed, trying fallback paths", err);
      }

      const fallbackPaths = [
        "/workers/rtcTransform.worker.js",
        "./workers/rtcTransform.worker.js",
        "/node_modules/@livepeer-frameworks/streamcrafter-core/dist/workers/rtcTransform.worker.js",
      ];

      for (const path of fallbackPaths) {
        try {
          return new Worker(path, { type: "module" });
        } catch {
          try {
            return new Worker(path);
          } catch {
            // Continue
          }
        }
      }

      return null;
    };

    // Get senders
    const senders = this.peerConnection.getSenders();
    const videoSender = senders.find((s) => s.track?.kind === "video");
    const audioSender = senders.find((s) => s.track?.kind === "audio");

    // Attach video transform
    if (videoSender && "transform" in videoSender) {
      this.log("Creating video transform worker");
      this.videoTransformWorker = createWorker();

      if (this.videoTransformWorker) {
        // Configure worker
        this.videoTransformWorker.postMessage({
          type: "configure",
          config: {
            debug: this.config.debug,
            maxQueueSize: 30, // ~1 second at 30fps
          },
        });

        // Attach transform to sender
        // RTCRtpScriptTransform options are passed to worker via rtctransform event
        (videoSender as RTCRtpSender & { transform: unknown }).transform =
          new RTCRtpScriptTransform(this.videoTransformWorker, { kind: "video" });

        this.log("Video transform attached");
      } else {
        this.logError("Failed to create video transform worker");
      }
    }

    // Attach audio transform
    if (audioSender && "transform" in audioSender) {
      this.log("Creating audio transform worker");
      this.audioTransformWorker = createWorker();

      if (this.audioTransformWorker) {
        // Configure worker
        this.audioTransformWorker.postMessage({
          type: "configure",
          config: {
            debug: this.config.debug,
            maxQueueSize: 50, // Audio packets are smaller/more frequent
          },
        });

        // Attach transform to sender
        (audioSender as RTCRtpSender & { transform: unknown }).transform =
          new RTCRtpScriptTransform(this.audioTransformWorker, { kind: "audio" });

        this.log("Audio transform attached");
      } else {
        this.logError("Failed to create audio transform worker");
      }
    }

    // Forward encoded chunks from encoder to transform workers
    const handleVideoChunk = (chunk: EncodedVideoChunkData): void => {
      if (this.videoTransformWorker) {
        this.videoTransformWorker.postMessage(
          { type: "videoChunk", data: chunk },
          [chunk.data] // Transfer ArrayBuffer ownership
        );
      }
    };

    const handleAudioChunk = (chunk: EncodedAudioChunkData): void => {
      if (this.audioTransformWorker) {
        this.audioTransformWorker.postMessage(
          { type: "audioChunk", data: chunk },
          [chunk.data] // Transfer ArrayBuffer ownership
        );
      }
    };

    // Subscribe to encoder events
    encoderManager.on("videoChunk", handleVideoChunk);
    encoderManager.on("audioChunk", handleAudioChunk);

    // Store cleanup function
    this.encoderListenerCleanup = () => {
      encoderManager.off("videoChunk", handleVideoChunk);
      encoderManager.off("audioChunk", handleAudioChunk);
    };

    this.log("Encoder transform attached successfully");
  }

  /**
   * Check if encoder transform is attached
   */
  hasEncoderTransform(): boolean {
    return this.videoTransformWorker !== null || this.audioTransformWorker !== null;
  }

  /**
   * Detach encoder transform and stop workers
   */
  detachEncoderTransform(): void {
    // Clean up encoder event listeners
    if (this.encoderListenerCleanup) {
      this.encoderListenerCleanup();
      this.encoderListenerCleanup = null;
    }

    // Stop video transform worker
    if (this.videoTransformWorker) {
      this.videoTransformWorker.postMessage({ type: "stop" });
      this.videoTransformWorker.terminate();
      this.videoTransformWorker = null;
    }

    // Stop audio transform worker
    if (this.audioTransformWorker) {
      this.audioTransformWorker.postMessage({ type: "stop" });
      this.audioTransformWorker.terminate();
      this.audioTransformWorker = null;
    }

    this.log("Encoder transform detached");
  }

  /**
   * Clean up writers
   */
  private cleanupWriters(): void {
    if (this.videoWriter) {
      try {
        this.videoWriter.releaseLock();
      } catch {
        // Ignore
      }
      this.videoWriter = null;
    }

    if (this.audioWriter) {
      try {
        this.audioWriter.releaseLock();
      } catch {
        // Ignore
      }
      this.audioWriter = null;
    }

    this.videoWriteQueue = [];
    this.audioWriteQueue = [];
    this.isProcessingVideoQueue = false;
    this.isProcessingAudioQueue = false;
  }

  /**
   * Clean up resources
   */
  private cleanup(): void {
    this.cleanupWriters();
    this.detachEncoderTransform();

    if (this.videoTrackGenerator) {
      this.videoTrackGenerator.stop();
      this.videoTrackGenerator = null;
    }

    if (this.audioTrackGenerator) {
      this.audioTrackGenerator.stop();
      this.audioTrackGenerator = null;
    }

    if (this.peerConnection) {
      this.peerConnection.close();
      this.peerConnection = null;
    }

    // Clear resource URL (note: DELETE should be sent before cleanup if needed)
    this.resourceUrl = null;
  }

  /**
   * Disconnect from WHIP endpoint
   */
  async disconnect(): Promise<void> {
    this.log("Disconnecting WHIP");

    // Send DELETE to WHIP resource URL per RFC 9725
    if (this.resourceUrl) {
      try {
        this.log("Sending DELETE to WHIP resource:", this.resourceUrl);
        await fetch(this.resourceUrl, { method: "DELETE" });
      } catch (error) {
        // Don't block disconnect on DELETE failure
        this.log("Failed to delete WHIP resource (non-fatal)", error);
      }
      this.resourceUrl = null;
    }

    this.cleanup();
    this.setState("disconnected");
  }

  /**
   * Destroy the client
   */
  destroy(): void {
    this.cleanup();
    this.removeAllListeners();
  }
}
