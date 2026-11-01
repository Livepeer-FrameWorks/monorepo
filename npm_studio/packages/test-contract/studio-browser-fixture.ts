/**
 * A browser for driving a real IngestControllerV2 and WhipClient through the
 * drop-in components: fake devices, a fake peer connection and a fake WHIP
 * endpoint. Each capability can be left out to act as a browser without it.
 */

export interface StudioBrowserOptions {
  /** navigator.mediaDevices.getUserMedia */
  camera?: boolean;
  /** navigator.mediaDevices.getDisplayMedia */
  screen?: boolean;
  /** RTCPeerConnection */
  publish?: boolean;
  /** HTMLMediaElement.prototype.captureStream */
  mediaFile?: boolean;
  /** What getUserMedia does; defaults to a camera with a microphone. */
  getUserMedia?: () => Promise<unknown>;
}

export class FakeMediaStream {
  constructor(public tracks: FakeTrack[] = []) {}
  getTracks() {
    return this.tracks;
  }
  getVideoTracks() {
    return this.tracks.filter((track) => track.kind === "video");
  }
  getAudioTracks() {
    return this.tracks.filter((track) => track.kind === "audio");
  }
  addTrack(track: FakeTrack) {
    this.tracks.push(track);
  }
  removeTrack(track: FakeTrack) {
    this.tracks = this.tracks.filter((candidate) => candidate !== track);
  }
}

export class FakeTrack {
  id: string;
  label: string;
  kind: "video" | "audio";
  enabled = true;
  readyState = "live";
  constructor(kind: "video" | "audio") {
    this.kind = kind;
    this.id = `${kind}-track`;
    this.label = kind;
  }
  stop() {}
  addEventListener() {}
  getSettings() {
    return { deviceId: this.kind };
  }
  async applyConstraints() {}
}

export class FakePeerConnection {
  static all: FakePeerConnection[] = [];
  connectionState = "new";
  iceConnectionState = "new";
  iceGatheringState = "new";
  onconnectionstatechange: (() => void) | null = null;
  oniceconnectionstatechange: (() => void) | null = null;
  onicegatheringstatechange: (() => void) | null = null;
  onicecandidate: (() => void) | null = null;
  constructor() {
    FakePeerConnection.all.push(this);
  }
  addTrack() {}
  close() {}
  getTransceivers() {
    return [];
  }
  getSenders() {
    return [];
  }
  async getStats() {
    return new Map();
  }
  async createOffer() {
    return { type: "offer", sdp: "v=0" };
  }
  async setLocalDescription() {}
  async setRemoteDescription() {}
  moveTo(state: string) {
    this.connectionState = state;
    this.onconnectionstatechange?.();
  }
}

/** Stands in for AudioMixer, whose graph needs a real AudioContext. */
export class FakeAudioMixer {
  getState() {
    return null;
  }
  async initialize() {}
  async resume() {}
  on() {
    return () => {};
  }
  addSource() {}
  removeSource() {}
  setVolume() {}
  mute() {}
  unmute() {}
  getOutputTrack() {
    return null;
  }
  setMasterVolume() {}
  getMasterVolume() {
    return 1;
  }
  startLevelMonitoring() {}
  stopLevelMonitoring() {}
  destroy() {}
}

export const lastPeer = () => FakePeerConnection.all[FakePeerConnection.all.length - 1];

export const whipAccepts = () =>
  new Response("v=0", { status: 201, headers: { Location: "https://edge.example/resource" } });

export const whipRefuses = (status: number, statusText: string, body = "") =>
  new Response(body, { status, statusText });

/** Install the browser and return what to answer WHIP POSTs with, plus the way back. */
export function installStudioBrowser(options: StudioBrowserOptions = {}) {
  const { camera = true, screen = true, publish = true, mediaFile = true } = options;
  const global = globalThis as unknown as Record<string, unknown>;
  const mediaProto = HTMLMediaElement.prototype as unknown as Record<string, unknown>;
  const savedMediaDevices = Object.getOwnPropertyDescriptor(navigator, "mediaDevices");
  const saved = {
    MediaStream: global.MediaStream,
    RTCPeerConnection: global.RTCPeerConnection,
    fetch: global.fetch,
    play: mediaProto.play,
    captureStream: mediaProto.captureStream,
  };

  const endpoint = { respond: () => whipAccepts() as Response };

  FakePeerConnection.all = [];
  global.MediaStream = FakeMediaStream;
  global.fetch = async (_url: string, init?: RequestInit) =>
    init?.method === "POST" ? endpoint.respond() : new Response("", { status: 200 });
  mediaProto.play = async () => {};

  if (publish) global.RTCPeerConnection = FakePeerConnection;
  else delete global.RTCPeerConnection;

  if (mediaFile) mediaProto.captureStream = () => new FakeMediaStream([new FakeTrack("video")]);
  else delete mediaProto.captureStream;

  const mediaDevices: Record<string, unknown> = {
    addEventListener() {},
    enumerateDevices: async () => [],
  };
  if (camera) {
    mediaDevices.getUserMedia =
      options.getUserMedia ??
      (async () => new FakeMediaStream([new FakeTrack("video"), new FakeTrack("audio")]));
  }
  if (screen) {
    mediaDevices.getDisplayMedia = async () => new FakeMediaStream([new FakeTrack("video")]);
  }
  Object.defineProperty(navigator, "mediaDevices", {
    configurable: true,
    value: camera || screen ? mediaDevices : undefined,
  });

  return {
    endpoint,
    uninstall() {
      global.MediaStream = saved.MediaStream;
      global.RTCPeerConnection = saved.RTCPeerConnection;
      global.fetch = saved.fetch;
      mediaProto.play = saved.play;
      if (saved.captureStream) mediaProto.captureStream = saved.captureStream;
      else delete mediaProto.captureStream;
      if (savedMediaDevices) Object.defineProperty(navigator, "mediaDevices", savedMediaDevices);
      else delete (navigator as unknown as Record<string, unknown>).mediaDevices;
    },
  };
}
