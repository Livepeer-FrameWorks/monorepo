import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MediaFileSource } from "../src/core/MediaFileSource";

class FakeTrack {
  readyState = "live";
  constructor(public kind: string) {}
  stop() {
    this.readyState = "ended";
  }
}

class FakeStream {
  constructor(public tracks: FakeTrack[]) {}
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

function fakeVideo(options: { capture?: boolean; audio?: boolean; video?: boolean } = {}) {
  const { capture = true, audio = true, video = true } = options;
  const listeners = new Map<string, Set<() => void>>();
  const element = {
    muted: false,
    playsInline: false,
    preload: "",
    loop: false,
    paused: true,
    ended: false,
    src: "",
    videoWidth: video ? 640 : 0,
    videoHeight: video ? 360 : 0,
    duration: 8,
    currentTime: 0,
    error: null,
    addEventListener(type: string, fn: () => void) {
      if (!listeners.has(type)) listeners.set(type, new Set());
      listeners.get(type)!.add(fn);
    },
    removeEventListener(type: string, fn: () => void) {
      listeners.get(type)?.delete(fn);
    },
    dispatch(type: string) {
      for (const fn of [...(listeners.get(type) ?? [])]) fn();
    },
    removeAttribute: vi.fn(),
    load: vi.fn(),
    pause: vi.fn(),
    play: vi.fn(async () => {}),
    captureStream: undefined as undefined | (() => FakeStream),
  };
  if (capture) {
    element.captureStream = vi.fn(
      () =>
        new FakeStream([
          ...(video ? [new FakeTrack("video")] : []),
          ...(audio ? [new FakeTrack("audio")] : []),
        ])
    );
  }
  return element;
}

let video: ReturnType<typeof fakeVideo>;

async function load(file = new File(["x"], "clip.mp4")) {
  const source = new MediaFileSource();
  const loaded = source.loadFile(file);
  video.dispatch("loadedmetadata");
  await loaded;
  return source;
}

beforeEach(() => {
  video = fakeVideo();
  vi.stubGlobal("document", { createElement: () => video });
  vi.stubGlobal("URL", { createObjectURL: () => "blob:clip", revokeObjectURL: vi.fn() });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("MediaFileSource", () => {
  it("captures fresh tracks when a file that ran out plays again", async () => {
    const source = await load();
    const first = source.getStream() as unknown as FakeStream;
    const changed = vi.fn();
    source.on("streamChanged", changed);

    // Chromium ends the captured tracks when the element reaches the end of the file.
    first.getTracks().forEach((track) => track.stop());
    video.ended = true;
    video.dispatch("ended");
    source.play();

    const next = source.getStream() as unknown as FakeStream;
    expect(next).not.toBe(first);
    expect(next.getVideoTracks()[0].readyState).toBe("live");
    expect(changed).toHaveBeenCalledWith({ stream: next });
  });

  it("keeps its stream across pause and play", async () => {
    const source = await load();
    const first = source.getStream();

    source.pause();
    source.play();

    expect(source.getStream()).toBe(first);
  });

  it("rejects where the browser cannot capture from a media element", async () => {
    video = fakeVideo({ capture: false });
    const source = new MediaFileSource();

    const loaded = source.loadFile(new File(["x"], "clip.mp4"));
    let outcome: unknown = "pending";
    loaded.catch((error) => (outcome = error));
    try {
      video.dispatch("loadedmetadata");
    } catch {
      // A throw here is the listener failing outside the promise.
    }
    await new Promise((resolve) => setTimeout(resolve));

    expect(outcome).toBeInstanceOf(Error);
  });

  it("reports audio only for an audio file", async () => {
    video = fakeVideo({ video: false });

    const source = await load(new File(["x"], "song.mp3"));

    expect(source.hasVideo).toBe(false);
    expect(source.hasAudio).toBe(true);
    expect((source.getStream() as unknown as FakeStream).getVideoTracks()).toEqual([]);
  });
});
