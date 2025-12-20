/**
 * Global type declarations for WebCodecs APIs
 * These types are not yet in the standard TypeScript DOM lib
 */

// MediaStreamTrackProcessor and Generator (Insertable Streams API)
declare class MediaStreamTrackProcessor<T extends VideoFrame | AudioData = VideoFrame | AudioData> {
  constructor(init: { track: MediaStreamTrack });
  readonly readable: ReadableStream<T>;
}

declare class MediaStreamTrackGenerator extends MediaStreamTrack {
  constructor(init: { kind: 'audio' | 'video' });
  readonly writable: WritableStream<VideoFrame | AudioData>;
}

// Extend VideoFrame with closed property
interface VideoFrame {
  readonly closed?: boolean;
}

// Extend AudioData with closed property
interface AudioData {
  readonly closed?: boolean;
}

// DisplayMediaStreamOptions extensions
interface DisplayMediaStreamOptions {
  video?: boolean | MediaTrackConstraints | {
    cursor?: 'always' | 'motion' | 'never';
    displaySurface?: 'application' | 'browser' | 'monitor' | 'window';
  };
  audio?: boolean | MediaTrackConstraints | {
    systemAudio?: 'include' | 'exclude';
  };
  preferCurrentTab?: boolean;
}

// RTCRtpScriptTransform API
declare class RTCRtpScriptTransform {
  constructor(worker: Worker, options?: unknown);
}

// Note: Worker bundling is handled via Rollup worker entrypoints in dist/workers.
