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
  createDataChannel = vi.fn(() => {
    lastDataChannel = new FakeDataChannel();
    return lastDataChannel as unknown as RTCDataChannel;
  });
  addTransceiver = vi.fn();
  createOffer = vi.fn(async () => ({ type: "offer", sdp: "offer-sdp" }));
  setLocalDescription = vi.fn(async () => {});
  setRemoteDescription = vi.fn(async () => {});
  close = vi.fn();
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
    volume: 1,
    buffered: { length: 0, start: vi.fn(), end: vi.fn() },
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
  const originalRTCPeerConnection = globalThis.RTCPeerConnection;
  const originalRTCSessionDescription = globalThis.RTCSessionDescription;

  beforeEach(() => {
    lastDataChannel = null;
    const video = makeVideoElement();
    vi.stubGlobal("document", {
      createElement: vi.fn((tag: string) => {
        if (tag === "video") return video;
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
    Object.defineProperty(globalThis, "RTCSessionDescription", {
      value: originalRTCSessionDescription,
      configurable: true,
      writable: true,
    });
    vi.restoreAllMocks();
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

    expect(lastDataChannel).not.toBeNull();
    expect(lastDataChannel?.sent).toEqual([]);

    lastDataChannel?.dispatch("open");

    expect(lastDataChannel?.sent.map((message) => JSON.parse(message))).toContainEqual({
      type: "play",
    });
  });
});
