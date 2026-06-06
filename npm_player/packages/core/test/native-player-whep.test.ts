import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { NativePlayerImpl } from "../src/players/NativePlayer";
import type { StreamInfo } from "../src/core/PlayerInterface";

type Listener = (event?: any) => void;

class FakeDataChannel {
  readyState: RTCDataChannelState = "connecting";
  sent: string[] = [];
  private listeners = new Map<string, Listener[]>();

  addEventListener(type: string, listener: Listener): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.readyState = "closed";
    this.dispatch("close");
  }

  dispatch(type: string, event: any = {}): void {
    if (type === "open") this.readyState = "open";
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event);
    }
  }
}

class FakeRTCPeerConnection {
  sctp = {};
  iceConnectionState: RTCIceConnectionState = "new";
  ontrack: ((event: RTCTrackEvent) => void) | null = null;
  createDataChannel = vi.fn(() => {
    lastDataChannel = new FakeDataChannel();
    return lastDataChannel as unknown as RTCDataChannel;
  });
  addTransceiver = vi.fn();
  createOffer = vi.fn(async () => ({ type: "offer", sdp: "offer-sdp" }));
  setLocalDescription = vi.fn(async () => {});
  setRemoteDescription = vi.fn(async () => {
    if (autoEmitRemoteTrack) {
      this.emitRemoteTrack();
    }
  });
  close = vi.fn();

  constructor() {
    lastPeerConnection = this;
  }

  emitRemoteTrack(): void {
    const track = { id: "video-track" } as MediaStreamTrack;
    const stream = new MediaStream([track]);
    this.ontrack?.({ streams: [stream], track } as RTCTrackEvent);
  }
}

class FakeRTCSessionDescription {
  type: RTCSdpType;
  sdp: string;

  constructor(init: RTCSessionDescriptionInit) {
    this.type = init.type;
    this.sdp = init.sdp ?? "";
  }
}

let lastDataChannel: FakeDataChannel | null = null;
let lastPeerConnection: FakeRTCPeerConnection | null = null;
let autoEmitRemoteTrack = true;
let currentVideo: (HTMLVideoElement & { _fire?: (type: string) => void }) | null = null;

class FakeMediaStream {
  private tracks: MediaStreamTrack[];

  constructor(tracks: MediaStreamTrack[] = []) {
    this.tracks = [...tracks];
  }

  getTracks(): MediaStreamTrack[] {
    return this.tracks;
  }

  addTrack(track: MediaStreamTrack): void {
    this.tracks.push(track);
  }
}

function makeVideoElement(): HTMLVideoElement {
  const listeners = new Map<string, Listener[]>();
  const video = {
    classList: { add: vi.fn() },
    setAttribute: vi.fn(),
    addEventListener: vi.fn((type: string, listener: Listener) => {
      const list = listeners.get(type) ?? [];
      list.push(listener);
      listeners.set(type, list);
    }),
    removeEventListener: vi.fn(),
    appendChild: vi.fn(),
    play: vi.fn(async () => {
      (video as any).paused = false;
      for (const listener of listeners.get("play") ?? []) listener();
    }),
    pause: vi.fn(() => {
      (video as any).paused = true;
      for (const listener of listeners.get("pause") ?? []) listener();
    }),
    load: vi.fn(),
    removeAttribute: vi.fn(),
    paused: true,
    muted: false,
    controls: false,
    loop: false,
    autoplay: false,
    currentTime: 0,
    duration: 0,
    readyState: 0,
    volume: 1,
    buffered: { length: 0, start: vi.fn(), end: vi.fn() },
    _fire(type: string) {
      for (const listener of listeners.get(type) ?? []) listener(new Event(type));
    },
  };
  return video as unknown as HTMLVideoElement;
}

function makeContainer() {
  return {
    classList: { add: vi.fn() },
    appendChild: vi.fn(),
    removeChild: vi.fn(),
    contains: vi.fn(() => true),
    innerHTML: "",
  } as unknown as HTMLElement;
}

const streamInfo: StreamInfo = {
  source: [{ type: "whep", url: "https://mist.example.test/view/webrtc/live" }],
  meta: { tracks: [{ type: "video", codec: "H264" }] },
  type: "live",
};

describe("NativePlayer WHEP control channel", () => {
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  const originalMediaStream = globalThis.MediaStream;
  const originalRTCPeerConnection = globalThis.RTCPeerConnection;
  const originalRTCSessionDescription = globalThis.RTCSessionDescription;

  beforeEach(() => {
    lastDataChannel = null;
    lastPeerConnection = null;
    autoEmitRemoteTrack = true;
    currentVideo = makeVideoElement() as HTMLVideoElement & { _fire?: (type: string) => void };
    vi.stubGlobal("document", {
      createElement: vi.fn((tag: string) => {
        if (tag === "video") return currentVideo;
        return {};
      }),
      pictureInPictureElement: null,
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        text: async () => "answer-sdp",
        headers: { get: vi.fn(() => null) },
      }))
    );
    vi.stubGlobal("MediaStream", FakeMediaStream);
    vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection);
    vi.stubGlobal("RTCSessionDescription", FakeRTCSessionDescription);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    Object.defineProperty(globalThis, "document", {
      value: originalDocument,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(globalThis, "fetch", {
      value: originalFetch,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(globalThis, "RTCPeerConnection", {
      value: originalRTCPeerConnection,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(globalThis, "MediaStream", {
      value: originalMediaStream,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(globalThis, "RTCSessionDescription", {
      value: originalRTCSessionDescription,
      configurable: true,
      writable: true,
    });
    vi.restoreAllMocks();
  });

  it("waits for remote WHEP media before emitting ready", async () => {
    autoEmitRemoteTrack = false;
    const player = new NativePlayerImpl();
    const container = makeContainer();
    const onReady = vi.fn();

    const initialized = player.initialize(
      container,
      { type: "whep", url: "https://mist.example.test/view/webrtc/live" },
      {
        autoplay: true,
        muted: true,
        controls: false,
        onReady,
      },
      streamInfo
    );

    await vi.waitFor(() => expect(lastPeerConnection?.setRemoteDescription).toHaveBeenCalled());
    expect(onReady).not.toHaveBeenCalled();

    lastPeerConnection?.emitRemoteTrack();

    await initialized;
    expect(onReady).not.toHaveBeenCalled();
    currentVideo?._fire?.("loadedmetadata");

    expect(onReady).toHaveBeenCalledTimes(1);
  });

  it("replays a play request made before the MistControl channel exists", async () => {
    const player = new NativePlayerImpl();
    const container = makeContainer();

    await player.initialize(
      container,
      { type: "whep", url: "https://mist.example.test/view/webrtc/live" },
      {
        autoplay: true,
        muted: true,
        controls: false,
        onReady: () => {
          void player.play();
        },
      },
      streamInfo
    );
    currentVideo?._fire?.("loadedmetadata");

    expect(lastDataChannel).not.toBeNull();
    expect(lastDataChannel?.sent).toEqual([]);

    lastDataChannel?.dispatch("open");

    expect(lastDataChannel?.sent.map((message) => JSON.parse(message))).toContainEqual({
      type: "play",
    });
  });
});
