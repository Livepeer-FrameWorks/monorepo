import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { IngestControllerV2 } from "../src/core/IngestControllerV2";
import { SceneManager } from "../src/core/SceneManager";
import type { MediaFileSource } from "../src/core/MediaFileSource";
import { isSourceVisibleInScene, type IngestState, type MediaSource } from "../src/types";

vi.mock("../src/core/WhipClient", () => ({
  WhipClient: class {
    on() {
      return () => {};
    }
    connect = vi.fn();
    disconnect = vi.fn();
    destroy = vi.fn();
  },
}));

class FakeMediaStream {
  constructor(public tracks: MediaStreamTrack[] = []) {}
  getTracks() {
    return this.tracks;
  }
  getVideoTracks() {
    return this.tracks.filter((track) => track.kind === "video");
  }
  getAudioTracks() {
    return this.tracks.filter((track) => track.kind === "audio");
  }
  addTrack(track: MediaStreamTrack) {
    this.tracks.push(track);
  }
  removeTrack(track: MediaStreamTrack) {
    this.tracks = this.tracks.filter((candidate) => candidate !== track);
  }
}

function track(kind: "video" | "audio", deviceId = kind) {
  return {
    kind,
    id: `${kind}-${deviceId}`,
    label: deviceId,
    enabled: true,
    readyState: "live",
    stop: vi.fn(),
    addEventListener: vi.fn(),
    getSettings: () => ({ deviceId }),
    applyConstraints: vi.fn(async () => {}),
  } as unknown as MediaStreamTrack;
}

function cameraStream(deviceId: string, withAudio = true) {
  return new FakeMediaStream([
    track("video", deviceId),
    ...(withAudio ? [track("audio", "mic")] : []),
  ]) as unknown as MediaStream;
}

const getUserMedia = vi.fn();
const enumerateDevices = vi.fn();
const controllers: IngestControllerV2[] = [];

function create(audioMixing = false) {
  const controller = new IngestControllerV2({
    whipUrl: "https://edge.example/webrtc/key",
    useWebCodecs: false,
    audioMixing,
  });
  controllers.push(controller);
  return controller;
}

function withCompositor(controller: IngestControllerV2) {
  const sceneManager = new SceneManager();
  const posted: { type: string; sourceId?: string }[] = [];
  const internals = sceneManager as unknown as {
    worker: unknown;
    workerReady: boolean;
    outputCanvas: unknown;
  };
  internals.worker = {
    postMessage: (message: { type: string }) => posted.push(message),
    terminate: vi.fn(),
  };
  internals.workerReady = true;
  sceneManager.setActiveScene(sceneManager.createScene("Main").id);
  (controller as unknown as { sceneManager: SceneManager }).sceneManager = sceneManager;
  return { sceneManager, posted };
}

function fakeMediaFile(stream: MediaStream) {
  const handlers = new Map<string, (event: unknown) => void>();
  return {
    stream,
    hasVideo: stream.getVideoTracks().length > 0,
    hasAudio: stream.getAudioTracks().length > 0,
    loop: false,
    isPlaying: false,
    duration: 8,
    getStream() {
      return this.stream;
    },
    on(event: string, handler: (event: unknown) => void) {
      handlers.set(event, handler);
      return () => handlers.delete(event);
    },
    emit(event: string, payload?: unknown) {
      handlers.get(event)?.(payload);
    },
    play: vi.fn(),
    pause: vi.fn(),
    destroy: vi.fn(),
  };
}

beforeEach(() => {
  vi.spyOn(console, "log").mockImplementation(() => {});
  getUserMedia.mockReset();
  enumerateDevices.mockReset();
  vi.stubGlobal("MediaStream", FakeMediaStream);
  vi.stubGlobal("navigator", {
    mediaDevices: { getUserMedia, enumerateDevices, addEventListener: vi.fn() },
  });
});

afterEach(() => {
  controllers.splice(0).forEach((controller) => controller.destroy());
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("more than one camera", () => {
  it("opens the second camera without a second microphone", async () => {
    getUserMedia
      .mockResolvedValueOnce(cameraStream("front"))
      .mockResolvedValueOnce(cameraStream("rear", false));
    const controller = create();

    const first = await controller.startCamera();
    const second = await controller.startCamera({ videoDeviceId: "rear" });

    expect(getUserMedia.mock.calls[1][0].audio).toBe(false);
    expect(controller.getSources().map((source) => source.id)).toEqual([first.id, second.id]);
    expect(first.stream.getVideoTracks()[0].stop).not.toHaveBeenCalled();
    expect(first.primaryVideo).toBe(true);
    expect(second.primaryVideo).toBe(false);
  });

  it("still reports the microphone after a camera without audio", async () => {
    getUserMedia
      .mockResolvedValueOnce(cameraStream("front"))
      .mockResolvedValueOnce(cameraStream("rear", false));
    const controller = create();

    await controller.startCamera();
    await controller.startCamera({ videoDeviceId: "rear" });

    expect(controller.getStateContext().hasAudio).toBe(true);
  });

  it("opens the picked camera when encoder overrides shape the capture", async () => {
    getUserMedia.mockResolvedValue(cameraStream("rear"));
    const controller = create();
    controller.setEncoderOverrides({ video: { width: 1280, height: 720 } });

    await controller.startCamera({ videoDeviceId: "rear" });

    expect(getUserMedia.mock.calls[0][0].video.deviceId).toEqual({ exact: "rear" });
  });

  it("does not swap in another camera when the picked one cannot meet the constraints", async () => {
    const overconstrained = new Error("Overconstrained");
    overconstrained.name = "OverconstrainedError";
    getUserMedia.mockRejectedValueOnce(overconstrained).mockResolvedValueOnce(cameraStream("rear"));
    const controller = create();

    await controller.startCamera({ videoDeviceId: "rear" });

    expect(getUserMedia.mock.calls[1][0].video.deviceId).toEqual({ exact: "rear" });
  });

  it("keeps the broadcast ready when a further camera fails to open", async () => {
    getUserMedia
      .mockResolvedValueOnce(cameraStream("front"))
      .mockRejectedValueOnce(new DOMException("Device in use", "NotReadableError"));
    const controller = create();
    const errors = vi.fn();
    controller.on("error", errors);
    await controller.startCamera();

    await expect(controller.startCamera({ videoDeviceId: "rear" })).rejects.toThrow();

    expect(controller.getState()).toBe("capturing");
    expect(errors).toHaveBeenCalledWith({ error: "Device in use", recoverable: true });
  });

  it("offers each video input once and marks the ones in use", async () => {
    getUserMedia.mockResolvedValue(cameraStream("front"));
    enumerateDevices.mockResolvedValue([
      { kind: "videoinput", deviceId: "front", label: "Front", groupId: "a" },
      { kind: "videoinput", deviceId: "rear", label: "Rear", groupId: "b" },
      { kind: "audioinput", deviceId: "mic", label: "Mic", groupId: "a" },
    ]);
    const controller = create();
    await controller.startCamera();

    expect(await controller.getCameraChoices()).toEqual([
      { deviceId: "front", label: "Front", inUse: true },
      { deviceId: "rear", label: "Rear", inUse: false },
    ]);
  });

  it("offers nothing to pick from before the browser has shown device ids", async () => {
    enumerateDevices.mockResolvedValue([
      { kind: "videoinput", deviceId: "", label: "", groupId: "" },
    ]);

    expect(await create().getCameraChoices()).toEqual([]);
  });

  it("switches the device of the camera it was told to and refreshes its compositor feed", async () => {
    getUserMedia
      .mockResolvedValueOnce(cameraStream("front"))
      .mockResolvedValueOnce(cameraStream("rear", false))
      .mockResolvedValueOnce(cameraStream("side", false));
    const controller = create();
    const first = await controller.startCamera();
    await controller.startCamera({ videoDeviceId: "rear" });
    const { sceneManager } = withCompositor(controller);
    const bind = vi.spyOn(sceneManager, "bindSource").mockImplementation(() => {});

    await controller.switchVideoDevice("side", first.id);

    expect(first.stream.getVideoTracks()[0].label).toBe("side");
    expect(bind).toHaveBeenCalledWith(first.id, first.stream);
  });

  it("leaves no stream behind to switch once the last camera is removed", async () => {
    getUserMedia.mockResolvedValueOnce(cameraStream("front"));
    const controller = create();
    const first = await controller.startCamera();

    controller.removeSource(first.id);

    await expect(controller.switchVideoDevice("rear")).rejects.toThrow();
    expect(getUserMedia).toHaveBeenCalledTimes(1);
  });
});

describe("media file sources", () => {
  it("binds an audio-only source without asking the compositor for video", () => {
    const controller = create();
    const { sceneManager } = withCompositor(controller);

    const source = controller.addCustomSource(
      new FakeMediaStream([track("audio")]) as unknown as MediaStream,
      "song.mp3"
    );

    expect(source.primaryVideo).toBe(false);
    expect(sceneManager.getActiveScene()!.layers).toEqual([]);
  });

  it("does not count an audio-only source as hidden from the scene", () => {
    const audio = { id: "song", stream: new FakeMediaStream([track("audio")]) };
    const scene = { layers: [] };

    expect(isSourceVisibleInScene(audio as unknown as MediaSource, scene)).toBe(true);
    expect(
      isSourceVisibleInScene(
        { id: "cam", stream: cameraStream("cam") } as unknown as MediaSource,
        scene
      )
    ).toBe(false);
  });

  it("keeps a file that ran out in the mix so it can play again", () => {
    const controller = create();
    const file = fakeMediaFile(cameraStream("clip"));

    const source = controller.addMediaFileSource(file as unknown as MediaFileSource, "clip.mp4")!;
    file.emit("ended");

    expect(controller.getSource(source.id)).toBeDefined();
    expect(source.type).toBe("media");
    expect(controller.getSource(source.id)!.media).toMatchObject({ playing: false });
  });

  it("swaps the fresh capture into the compositor and mixer when the file plays again", () => {
    const controller = create();
    const { sceneManager } = withCompositor(controller);
    const bind = vi.spyOn(sceneManager, "bindSource").mockImplementation(() => {});
    const file = fakeMediaFile(cameraStream("clip"));
    const source = controller.addMediaFileSource(file as unknown as MediaFileSource, "clip.mp4")!;
    bind.mockClear();

    const next = cameraStream("clip-again");
    file.stream = next;
    file.emit("streamChanged", { stream: next });

    expect(controller.getSource(source.id)!.stream).toBe(next);
    expect(bind).toHaveBeenCalledWith(source.id, next);
    expect(sceneManager.getActiveScene()!.layers).toHaveLength(1);
  });

  it("keeps volume and mute in the mixer across a fresh capture", () => {
    const controller = create(true);
    const mixer = controller.getAudioMixer();
    const add = vi.spyOn(mixer, "addSource").mockImplementation(() => {});
    vi.spyOn(mixer, "removeSource").mockImplementation(() => {});
    vi.spyOn(mixer, "setVolume").mockImplementation(() => {});
    vi.spyOn(mixer, "mute").mockImplementation(() => {});
    const file = fakeMediaFile(cameraStream("clip"));
    const source = controller.addMediaFileSource(file as unknown as MediaFileSource, "clip.mp4")!;
    controller.setSourceVolume(source.id, 0.4);
    controller.setSourceMuted(source.id, true);
    add.mockClear();

    const next = cameraStream("clip-again");
    file.stream = next;
    file.emit("streamChanged", { stream: next });

    expect(add).toHaveBeenCalledWith(source.id, next.getAudioTracks()[0], {
      volume: 0.4,
      muted: true,
    });
  });

  it("reports play, pause and loop, and lets go of the file when removed", () => {
    const controller = create();
    const file = fakeMediaFile(cameraStream("clip"));
    const updates = vi.fn();
    controller.on("sourceUpdated", updates);
    const source = controller.addMediaFileSource(file as unknown as MediaFileSource, "clip.mp4")!;

    controller.playMediaSource(source.id);
    file.isPlaying = true;
    file.emit("playing");
    expect(controller.getSource(source.id)!.media).toMatchObject({ playing: true });
    expect(updates).toHaveBeenCalled();

    controller.pauseMediaSource(source.id);
    file.isPlaying = false;
    file.emit("paused");
    expect(controller.getSource(source.id)!.media).toMatchObject({ playing: false });

    controller.setMediaSourceLoop(source.id, true);
    expect(file.loop).toBe(true);
    expect(controller.getSource(source.id)!.media).toMatchObject({ loop: true });

    controller.removeSource(source.id);
    expect(file.destroy).toHaveBeenCalled();
  });

  it("moves to capturing so a file alone can go live", () => {
    const controller = create();
    const file = fakeMediaFile(cameraStream("clip"));

    controller.addMediaFileSource(file as unknown as MediaFileSource, "clip.mp4");

    expect(controller.getState() satisfies IngestState).toBe("capturing");
  });
});

describe("source removal", () => {
  it("releases the source's last frame in the worker", () => {
    const controller = create();
    const { posted } = withCompositor(controller);
    const source = controller.addCustomSource(
      new FakeMediaStream([track("audio")]) as unknown as MediaStream,
      "song.mp3"
    );

    controller.removeSource(source.id);

    expect(posted).toContainEqual({ type: "removeSource", sourceId: source.id });
  });
});
