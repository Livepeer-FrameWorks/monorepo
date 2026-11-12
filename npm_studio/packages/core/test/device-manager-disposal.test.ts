import { afterEach, describe, expect, it, vi } from "vitest";
import { DeviceManager } from "../src/core/DeviceManager";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

function mediaFixture() {
  const media = Object.assign(new EventTarget(), {
    enumerateDevices: vi.fn(async (): Promise<MediaDeviceInfo[]> => []),
    getUserMedia: vi.fn(async (): Promise<MediaStream> => capture().stream),
  });
  vi.stubGlobal("navigator", { mediaDevices: media });
  return media;
}

function capture() {
  const stop = vi.fn();
  const track = { kind: "video", stop } as unknown as MediaStreamTrack;
  const stream = {
    getTracks: () => [track],
    getVideoTracks: () => [track],
    getAudioTracks: () => [],
  } as unknown as MediaStream;
  return { stream, stop };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("DeviceManager disposal", () => {
  it("keeps a controller destroyed when a pending camera is granted", async () => {
    const media = mediaFixture();
    const permission = deferred<MediaStream>();
    const started = deferred<void>();
    media.getUserMedia.mockImplementation(() => {
      started.resolve();
      return permission.promise;
    });
    const controller = new IngestControllerV2({
      whipUrl: "https://whip.example/ingest",
      useWebCodecs: false,
      audioMixing: false,
    });
    const pending = controller.startCamera().catch(() => {});
    await started.promise;
    controller.destroy();
    const late = capture();
    permission.resolve(late.stream);
    await pending;
    expect(late.stop).toHaveBeenCalledOnce();
    expect(controller.getState()).toBe("destroyed");
    expect(controller.getSources()).toEqual([]);
  });
  it("removes the exact browser listener once on each mount and teardown", async () => {
    const media = mediaFixture();
    const add = vi.spyOn(media, "addEventListener");
    const remove = vi.spyOn(media, "removeEventListener");
    for (let cycle = 0; cycle < 10; cycle++) {
      const manager = new DeviceManager();
      manager.destroy();
      manager.destroy();
    }
    media.dispatchEvent(new Event("devicechange"));
    await Promise.resolve();
    expect(remove).toHaveBeenCalledTimes(10);
    for (const [index, call] of add.mock.calls.entries()) {
      expect(remove.mock.calls[index]).toEqual(call);
    }
    expect(media.enumerateDevices).not.toHaveBeenCalled();
  });

  it("discards an enumeration already in flight when destroyed", async () => {
    const media = mediaFixture();
    const pending = deferred<MediaDeviceInfo[]>();
    media.enumerateDevices.mockReturnValueOnce(pending.promise);
    const manager = new DeviceManager();
    const emit = vi.spyOn(manager, "emit");
    media.dispatchEvent(new Event("devicechange"));
    manager.destroy();
    pending.resolve([
      {
        deviceId: "camera",
        groupId: "camera",
        kind: "videoinput",
        label: "Camera",
      } as MediaDeviceInfo,
    ]);
    await pending.promise;
    await Promise.resolve();
    await Promise.resolve();
    expect(manager.getAllDevices()).toEqual([]);
    expect(emit).not.toHaveBeenCalled();
  });

  it("stops late camera tracks instead of attaching them after teardown", async () => {
    const media = mediaFixture();
    const pending = deferred<MediaStream>();
    media.getUserMedia.mockReturnValueOnce(pending.promise);
    const manager = new DeviceManager();
    const opening = manager.getUserMedia();
    manager.destroy();
    const late = capture();
    pending.resolve(late.stream);
    await expect(opening).rejects.toMatchObject({ name: "AbortError" });
    expect(late.stop).toHaveBeenCalledOnce();
    expect(manager.getStream()).toBeNull();
  });

  it("does not reopen a fallback camera after disposal", async () => {
    const media = mediaFixture();
    const pending = deferred<MediaStream>();
    media.getUserMedia.mockReturnValueOnce(pending.promise);
    const manager = new DeviceManager();
    const opening = manager.getUserMedia();
    manager.destroy();
    pending.reject(new DOMException("camera constraints unavailable", "OverconstrainedError"));
    await expect(opening).rejects.toMatchObject({ name: "AbortError" });
    expect(media.getUserMedia).toHaveBeenCalledOnce();
  });

  it("stops a late permission capture without publishing permission changes", async () => {
    const media = mediaFixture();
    const pending = deferred<MediaStream>();
    media.getUserMedia.mockReturnValueOnce(pending.promise);
    const manager = new DeviceManager();
    const emit = vi.spyOn(manager, "emit");
    const permission = manager.requestPermissions();
    manager.destroy();
    const late = capture();
    pending.resolve(late.stream);
    await expect(permission).rejects.toMatchObject({ name: "AbortError" });
    expect(late.stop).toHaveBeenCalledOnce();
    expect(media.enumerateDevices).not.toHaveBeenCalled();
    expect(emit).not.toHaveBeenCalled();
  });
});
