import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MistWebRTCPlayerImpl } from "../src/players/MistWebRTCPlayer";

type Listener = (event?: Event) => void;

let sentCommands: Array<Record<string, unknown>> = [];
let createdTags: string[] = [];
let peerConnectionSetup: string[] = [];

class FakeWebSocket {
  static OPEN = 1;
  static CONNECTING = 0;
  readyState = FakeWebSocket.CONNECTING;
  binaryType = "";
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: { code: number }) => void) | null = null;

  constructor(_url: string) {
    queueMicrotask(() => {
      this.readyState = FakeWebSocket.OPEN;
      this.onopen?.();
    });
  }

  send(raw: string): void {
    const command = JSON.parse(raw) as Record<string, unknown>;
    sentCommands.push(command);
    if (command.type === "offer_sdp") {
      this.onmessage?.({
        data: JSON.stringify({
          type: "on_answer_sdp",
          result: true,
          answer_sdp: "v=0\r\n",
        }),
      });
    }
  }

  close(): void {
    this.readyState = 3;
    this.onclose?.({ code: 1000 });
  }
}

class FakeRTCPeerConnection {
  connectionState: RTCPeerConnectionState = "new";
  iceConnectionState: RTCIceConnectionState = "new";
  onconnectionstatechange: (() => void) | null = null;
  oniceconnectionstatechange: (() => void) | null = null;
  ontrack: ((event: RTCTrackEvent) => void) | null = null;
  createDataChannel = vi.fn(() => {
    peerConnectionSetup.push("data");
    return { close: vi.fn(), onmessage: null } as unknown as RTCDataChannel;
  });
  addTransceiver = vi.fn((kind: string) => {
    peerConnectionSetup.push(kind);
  });
  createOffer = vi.fn(async () => ({ type: "offer" as RTCSdpType, sdp: "v=0\r\n" }));
  setLocalDescription = vi.fn(async () => {});
  setRemoteDescription = vi.fn(async () => {});
  close = vi.fn();
}

function fakeVideo(): HTMLVideoElement {
  const listeners = new Map<string, Listener[]>();
  const video = {
    classList: { add: vi.fn() },
    setAttribute: vi.fn(),
    addEventListener: vi.fn((type: string, listener: Listener) => {
      listeners.set(type, [...(listeners.get(type) ?? []), listener]);
    }),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
    play: vi.fn(async () => {
      video.paused = false;
    }),
    pause: vi.fn(() => {
      video.paused = true;
    }),
    paused: true,
    readyState: 0,
    currentTime: 0,
    duration: 0,
    volume: 1,
    muted: false,
    controls: false,
    autoplay: false,
    loop: false,
    buffered: { length: 0 },
  };
  return video as unknown as HTMLVideoElement;
}

describe("Mist WebRTC startup", () => {
  beforeEach(() => {
    sentCommands = [];
    createdTags = [];
    peerConnectionSetup = [];
    const video = fakeVideo();
    vi.stubGlobal("window", { RTCPeerConnection: FakeRTCPeerConnection, WebSocket: FakeWebSocket });
    vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.stubGlobal("RTCPeerConnection", FakeRTCPeerConnection);
    vi.stubGlobal("RTCRtpReceiver", {
      getCapabilities: () => ({ codecs: [{ mimeType: "video/H264" }] }),
    });
    vi.stubGlobal("document", {
      createElement: (tag: string) => {
        createdTags.push(tag);
        return video;
      },
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("subscribes for the SDP answer and starts autoplay without waiting for media metadata", async () => {
    const player = new MistWebRTCPlayerImpl();
    const container = {
      classList: { add: vi.fn() },
      appendChild: vi.fn(),
    } as unknown as HTMLElement;

    await player.initialize(
      container,
      { type: "mist/webrtc", url: "wss://mist.example.test/view/webrtc/live" },
      { autoplay: true, muted: true, controls: false },
      {
        type: "live",
        source: [],
        meta: { tracks: [{ type: "video", codec: "H264" }] },
      }
    );

    expect(sentCommands.map((command) => command.type)).toEqual(["offer_sdp", "play"]);
    expect(createdTags).toEqual(["video"]);
  });

  it("places primary media before metadata in the BUNDLE offer", async () => {
    const player = new MistWebRTCPlayerImpl();
    const container = {
      classList: { add: vi.fn() },
      appendChild: vi.fn(),
    } as unknown as HTMLElement;

    await player.initialize(
      container,
      { type: "mist/webrtc", url: "wss://mist.example.test/view/webrtc/live" },
      { autoplay: true, muted: true, controls: false },
      {
        type: "live",
        source: [],
        meta: { tracks: [{ type: "video", codec: "H264" }] },
      }
    );

    expect(peerConnectionSetup).toEqual(["audio", "video", "data"]);
  });

  it("does not hold a click-to-play session before media metadata can arrive", async () => {
    const player = new MistWebRTCPlayerImpl();
    const container = {
      classList: { add: vi.fn() },
      appendChild: vi.fn(),
    } as unknown as HTMLElement;

    await player.initialize(
      container,
      { type: "mist/webrtc", url: "wss://mist.example.test/view/webrtc/live" },
      { autoplay: false, muted: true, controls: true },
      {
        type: "live",
        source: [],
        meta: { tracks: [{ type: "video", codec: "H264" }] },
      }
    );

    expect(sentCommands.map((command) => command.type)).toEqual(["offer_sdp"]);
  });
});
