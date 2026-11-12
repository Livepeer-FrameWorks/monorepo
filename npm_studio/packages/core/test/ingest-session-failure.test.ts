import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";
import type { IngestState, MediaSource } from "../src/types";

class FakePeerConnection {
  static all: FakePeerConnection[] = [];
  connectionState = "new";
  iceConnectionState = "new";
  iceGatheringState = "new";
  onconnectionstatechange: (() => void) | null = null;
  oniceconnectionstatechange: (() => void) | null = null;
  onicegatheringstatechange: (() => void) | null = null;
  onicecandidate: (() => void) | null = null;
  close = vi.fn();
  transceivers: {
    sender: {
      track: MediaStreamTrack | null;
      getParameters(): { codecs: [] };
      replaceTrack(track: MediaStreamTrack | null): Promise<void>;
    };
    receiver: { track: { kind: string } };
  }[] = [];
  constructor() {
    FakePeerConnection.all.push(this);
  }
  addTrack(track: MediaStreamTrack) {
    const sender = {
      track: track as MediaStreamTrack | null,
      getParameters: () => ({ codecs: [] as [] }),
      async replaceTrack(next: MediaStreamTrack | null) {
        sender.track = next;
      },
    };
    this.transceivers.push({ sender, receiver: { track: { kind: track.kind } } });
  }
  getTransceivers() {
    return this.transceivers;
  }
  getSenders() {
    return this.transceivers.map((transceiver) => transceiver.sender);
  }
  senderFor(kind: string) {
    return this.transceivers.find((t) => t.receiver.track.kind === kind)!.sender;
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

class FakeMediaStream {
  constructor(private tracks: MediaStreamTrack[] = []) {}
  addTrack(track: MediaStreamTrack) {
    this.tracks.push(track);
  }
  getTracks() {
    return this.tracks;
  }
  getVideoTracks() {
    return this.tracks.filter((track) => track.kind === "video");
  }
  getAudioTracks() {
    return this.tracks.filter((track) => track.kind === "audio");
  }
}

const accepted = () =>
  new Response("v=0", { status: 201, headers: { Location: "https://edge.example/resource" } });
const refused = (status: number) =>
  new Response("no capacity", { status, statusText: `Status ${status}` });

const fetcher = vi.fn();
const controllers: IngestControllerV2[] = [];

interface Internals {
  setState(next: IngestState): void;
  sources: Map<string, MediaSource>;
  outputStream: MediaStream | null;
  whipClient: unknown;
  encoderManager: unknown;
  statsInterval: unknown;
  handleEncoderFailure(): Promise<void>;
  reconnectionManager: {
    start(run: () => Promise<void>): void;
    isReconnecting(): boolean;
    getState(): { attemptNumber: number };
  };
}

function create(reconnection: { enabled?: boolean; maxAttempts?: number } = {}) {
  const controller = new IngestControllerV2({
    whipUrl: "https://edge.example/webrtc/key",
    useWebCodecs: false,
    audioMixing: false,
    reconnection: { enabled: true, maxAttempts: 3, ...reconnection },
  });
  controllers.push(controller);
  const internals = controller as unknown as Internals;
  const stream = new FakeMediaStream([
    { kind: "video", stop: vi.fn() } as unknown as MediaStreamTrack,
  ]) as unknown as MediaStream;
  internals.sources.set("camera", {
    id: "camera",
    type: "camera",
    stream,
    active: true,
    muted: false,
    volume: 1,
  } as MediaSource);
  internals.outputStream = stream;
  internals.setState("capturing");
  const errors: { error: string; recoverable: boolean }[] = [];
  controller.on("error", (event) => errors.push(event));
  return { controller, internals, errors };
}

const lastPeer = () => FakePeerConnection.all[FakePeerConnection.all.length - 1];

async function goLive(controller: IngestControllerV2) {
  fetcher.mockResolvedValueOnce(accepted());
  await controller.startStreaming();
  lastPeer().moveTo("connected");
  expect(controller.getState()).toBe("streaming");
}

beforeEach(() => {
  FakePeerConnection.all = [];
  fetcher.mockReset();
  fetcher.mockImplementation(async () => new Response("", { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  vi.stubGlobal("RTCPeerConnection", FakePeerConnection);
  vi.spyOn(console, "log").mockImplementation(() => {});
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  controllers.splice(0).forEach((controller) => controller.destroy());
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("a Go Live that fails", () => {
  it("reports the server's refusal once, with the status it gave", async () => {
    const { controller, errors } = create();
    fetcher.mockResolvedValueOnce(refused(403));

    await expect(controller.startStreaming()).rejects.toThrow("403");

    expect(errors).toEqual([
      { error: "WHIP request failed: 403 Status 403 - no capacity", recoverable: true },
    ]);
    expect(controller.getState()).toBe("capturing");
    expect(controller.getStateContext().error).toContain("403");
  });

  it("reports a connection that fails after the server accepted it", async () => {
    const { controller, errors } = create();
    fetcher.mockResolvedValueOnce(accepted());
    await controller.startStreaming();

    lastPeer().moveTo("failed");

    expect(errors).toEqual([{ error: "Connection failed", recoverable: true }]);
    expect(controller.getState()).toBe("capturing");
  });

  it("stays quiet about a connect the broadcaster stopped", async () => {
    const { controller, errors } = create();
    let answer: (response: Response) => void = () => {};
    fetcher.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          answer = resolve;
        })
    );
    const started = controller.startStreaming();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalled());

    await controller.stopStreaming();
    answer(refused(503));

    await expect(started).rejects.toMatchObject({ name: "AbortError" });
    expect(errors).toEqual([]);
    expect(controller.getState()).toBe("capturing");
  });

  it("says the error cannot be retried when nothing is left to publish", async () => {
    const { controller, internals, errors } = create({ enabled: false });
    await goLive(controller);
    internals.sources.clear();

    lastPeer().moveTo("failed");

    expect(errors).toEqual([{ error: "Connection failed", recoverable: false }]);
    expect(controller.getState()).toBe("error");
  });
});

describe("reconnecting after a lost connection", () => {
  it("reports running out of attempts with the last reason", async () => {
    vi.useFakeTimers();
    const { controller, errors } = create();
    await goLive(controller);
    fetcher.mockImplementation(async () => refused(503));

    lastPeer().moveTo("failed");
    await vi.advanceTimersByTimeAsync(60_000);

    expect(errors).toEqual([
      {
        error:
          "Connection lost - reconnection failed: WHIP request failed: 503 Status 503 - no capacity",
        recoverable: true,
      },
    ]);
    expect(controller.getState()).toBe("capturing");
    expect(controller.getStateContext().reconnection?.isReconnecting).toBe(false);
  });

  it("stays reconnecting and keeps Go Live away when an attempt's connect is refused", async () => {
    vi.useFakeTimers();
    const { controller, internals, errors } = create();
    await goLive(controller);
    fetcher.mockImplementation(async () => refused(503));

    lastPeer().moveTo("failed");
    await vi.advanceTimersByTimeAsync(1_500);

    expect(controller.getState()).toBe("reconnecting");
    expect(controller.isCapturing()).toBe(false);
    expect(internals.reconnectionManager.isReconnecting()).toBe(true);
    await expect(controller.startStreaming()).rejects.toThrow("current state");
    expect(errors).toEqual([]);
  });

  it("stays reconnecting when an attempt's connection fails after the server accepted it", async () => {
    vi.useFakeTimers();
    const { controller, internals } = create();
    await goLive(controller);

    lastPeer().moveTo("failed");
    fetcher.mockResolvedValueOnce(accepted());
    await vi.advanceTimersByTimeAsync(1_500);
    lastPeer().moveTo("failed");
    await vi.advanceTimersByTimeAsync(0);

    expect(controller.getState()).toBe("reconnecting");
    expect(internals.reconnectionManager.isReconnecting()).toBe(true);
  });

  it("goes back to streaming when a later attempt connects", async () => {
    vi.useFakeTimers();
    const { controller } = create();
    await goLive(controller);
    const recovered = vi.fn();
    controller.on("reconnectionSuccess", recovered);

    lastPeer().moveTo("failed");
    fetcher.mockResolvedValueOnce(refused(503));
    await vi.advanceTimersByTimeAsync(1_500);
    fetcher.mockResolvedValueOnce(accepted());
    await vi.advanceTimersByTimeAsync(3_000);
    lastPeer().moveTo("connected");
    await vi.advanceTimersByTimeAsync(0);

    expect(controller.getState()).toBe("streaming");
    expect(recovered).toHaveBeenCalledOnce();
    expect(controller.getStateContext().reconnection?.isReconnecting).toBe(false);
  });

  it("leaves a retry still waiting no way to run into a new session", async () => {
    vi.useFakeTimers();
    const { controller, internals } = create();
    const staleAttempt = vi.fn(async () => {});
    internals.reconnectionManager.start(staleAttempt);
    fetcher.mockResolvedValueOnce(accepted());

    await controller.startStreaming();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(staleAttempt).not.toHaveBeenCalled();
  });

  it("does not go live from an attempt that was running when the broadcaster stopped", async () => {
    vi.useFakeTimers();
    const { controller, internals } = create();
    await goLive(controller);
    fetcher.mockResolvedValueOnce(accepted());
    lastPeer().moveTo("failed");
    const peers = () => FakePeerConnection.all.length;

    // The attempt is held at the DELETE of the lost resource.
    let finishDelete: (response: Response) => void = () => {};
    fetcher.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          finishDelete = resolve;
        })
    );
    await vi.advanceTimersByTimeAsync(1_500);
    const peersDuringAttempt = peers();
    await controller.stopStreaming();
    finishDelete(new Response("", { status: 200 }));
    await vi.advanceTimersByTimeAsync(60_000);

    expect(controller.getState()).toBe("capturing");
    expect(peers()).toBe(peersDuringAttempt);
    expect(internals.whipClient).toBeNull();
  });

  it("stops retrying when capture stops", async () => {
    vi.useFakeTimers();
    const { controller, internals, errors } = create();
    await goLive(controller);
    lastPeer().moveTo("failed");

    await controller.stopCapture();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(internals.reconnectionManager.isReconnecting()).toBe(false);
    expect(controller.getState()).toBe("idle");
    expect(errors).toEqual([]);
  });

  it("hands a reconnect after an encoder failure to the retries when it fails too", async () => {
    vi.useFakeTimers();
    const { controller, internals } = create();
    await goLive(controller);
    fetcher.mockResolvedValueOnce(accepted());

    await internals.handleEncoderFailure();
    expect(controller.getState()).toBe("reconnecting");
    lastPeer().moveTo("failed");

    expect(internals.reconnectionManager.isReconnecting()).toBe(true);
  });
});

describe("the end of a session", () => {
  function withEncoder(internals: Internals) {
    const encoder = { stop: vi.fn(async () => {}), destroy: vi.fn() };
    internals.encoderManager = encoder;
    return encoder;
  }

  it("releases the encoder and connection of a session that failed", async () => {
    const { controller, internals } = create({ enabled: false });
    await goLive(controller);
    const encoder = withEncoder(internals);
    const peer = lastPeer();

    peer.moveTo("failed");
    await vi.waitFor(() => expect(encoder.destroy).toHaveBeenCalled());

    expect(encoder.stop).toHaveBeenCalled();
    expect(peer.close).toHaveBeenCalled();
    expect(internals.encoderManager).toBeNull();
    expect(internals.whipClient).toBeNull();
    expect(internals.statsInterval).toBeNull();
    expect(fetcher).toHaveBeenCalledWith("https://edge.example/resource", { method: "DELETE" });
  });

  it("releases them the same way when the broadcaster stops", async () => {
    const { controller, internals } = create();
    await goLive(controller);
    const encoder = withEncoder(internals);

    await controller.stopStreaming();

    expect(encoder.stop).toHaveBeenCalled();
    expect(encoder.destroy).toHaveBeenCalled();
    expect(internals.whipClient).toBeNull();
    expect(controller.getState()).toBe("capturing");
  });

  it("finishes stopping when the encoder will not stop cleanly", async () => {
    const { controller, internals } = create();
    await goLive(controller);
    const encoder = withEncoder(internals);
    encoder.stop.mockRejectedValue(new Error("worker gone"));
    const peer = lastPeer();

    await controller.stopStreaming();

    expect(encoder.destroy).toHaveBeenCalled();
    expect(peer.close).toHaveBeenCalled();
    expect(controller.getState()).toBe("capturing");
  });

  it("leaves the next session nothing of the failed one to orphan", async () => {
    const { controller, internals } = create({ enabled: false });
    await goLive(controller);
    const first = internals.whipClient;
    const peer = lastPeer();

    peer.moveTo("failed");
    await goLive(controller);

    expect(internals.whipClient).not.toBe(first);
    expect(peer.close).toHaveBeenCalled();
  });
});

describe("stopping while reconnecting", () => {
  it("ends the retries for good", async () => {
    vi.useFakeTimers();
    const { controller, internals } = create({ maxAttempts: 5 });
    await goLive(controller);
    fetcher.mockImplementation(async (_url, init) =>
      (init as RequestInit)?.method === "POST" ? refused(503) : new Response("")
    );
    lastPeer().moveTo("failed");
    await vi.advanceTimersByTimeAsync(1_500);
    const posts = () =>
      fetcher.mock.calls.filter(([, init]) => (init as RequestInit)?.method === "POST").length;
    const postsSoFar = posts();

    await controller.stopStreaming();
    await vi.advanceTimersByTimeAsync(120_000);

    expect(posts()).toBe(postsSoFar);
    expect(controller.getState()).toBe("capturing");
    expect(internals.reconnectionManager.isReconnecting()).toBe(false);
  });

  it("returns at once when the lost server never answers the release", async () => {
    vi.useFakeTimers();
    const { controller } = create();
    await goLive(controller);
    lastPeer().moveTo("failed");
    fetcher.mockImplementationOnce(() => new Promise<Response>(() => {}));

    void controller.stopStreaming();

    expect(controller.getState()).toBe("capturing");
  });

  it("does not reconnect after an encoder failure the broadcaster stopped", async () => {
    const { controller, internals } = create();
    await goLive(controller);
    let finishDelete: (response: Response) => void = () => {};
    fetcher.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          finishDelete = resolve;
        })
    );
    const peers = FakePeerConnection.all.length;

    const reconnect = internals.handleEncoderFailure();
    await controller.stopStreaming();
    finishDelete(new Response(""));
    await reconnect;

    expect(FakePeerConnection.all).toHaveLength(peers);
    expect(internals.whipClient).toBeNull();
    expect(controller.getState()).toBe("capturing");
  });
});

describe("a microphone reopened during a live session", () => {
  function fakeTrack(kind: "video" | "audio", deviceId: string) {
    return {
      kind,
      id: `${kind}-${deviceId}`,
      label: deviceId,
      enabled: true,
      stop: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      getSettings: () => ({ deviceId }),
    } as unknown as MediaStreamTrack;
  }

  it("reaches the audio sender after its track went null", async () => {
    vi.stubGlobal("MediaStream", FakeMediaStream);
    const newMic = fakeTrack("audio", "mic-2");
    const getUserMedia = vi
      .fn()
      .mockResolvedValueOnce(
        new FakeMediaStream([fakeTrack("video", "front"), fakeTrack("audio", "mic")])
      )
      .mockResolvedValueOnce(new FakeMediaStream([fakeTrack("video", "rear")]))
      .mockResolvedValueOnce(new FakeMediaStream([newMic]));
    vi.stubGlobal("navigator", {
      mediaDevices: { getUserMedia, addEventListener: vi.fn(), removeEventListener: vi.fn() },
    });
    const controller = new IngestControllerV2({
      whipUrl: "https://edge.example/webrtc/key",
      useWebCodecs: false,
      audioMixing: false,
      reconnection: { enabled: false },
    });
    controllers.push(controller);
    const front = await controller.startCamera();
    await controller.startCamera({ videoDeviceId: "rear" });
    await goLive(controller);
    const audioSender = lastPeer().senderFor("audio");

    controller.removeSource(front.id);

    await vi.waitFor(() => expect(audioSender.track).toBe(newMic));
  });
});

describe("a capture that fails with no source running", () => {
  it("leaves the studio usable for another source", async () => {
    const { controller, internals, errors } = create();
    internals.sources.clear();
    internals.setState("idle");
    const devices = (controller as unknown as { deviceManager: { getUserMedia: unknown } })
      .deviceManager;
    devices.getUserMedia = vi.fn(async () => {
      throw new Error("Requested device not found");
    });

    await expect(controller.startCamera()).rejects.toThrow("not found");

    expect(controller.getState()).toBe("idle");
    expect(errors).toEqual([{ error: "Requested device not found", recoverable: true }]);
    expect(controller.getStateContext().error).toBe("Requested device not found");
  });

  it("clears the reported error when the next capture starts", async () => {
    const { controller, internals } = create();
    internals.sources.clear();
    internals.setState("idle");
    const devices = (controller as unknown as { deviceManager: { getUserMedia: unknown } })
      .deviceManager;
    devices.getUserMedia = vi.fn(async () => {
      throw new Error("Requested device not found");
    });
    await controller.startCamera().catch(() => {});
    const screens = (controller as unknown as { screenCapture: { start: unknown } }).screenCapture;
    screens.start = vi.fn(async () => null);

    await controller.startScreenShare();

    expect(controller.getStateContext().error).toBeUndefined();
  });
});
