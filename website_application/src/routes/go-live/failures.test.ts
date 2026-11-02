import { describe, expect, it } from "vitest";
import { describeResolveFailure, describeStudioFailure } from "./failures";
import { DEFAULT_STUDIO_SETTINGS, loadStudioSettings, saveStudioSettings } from "./studio-settings";

describe("Go Live failure guidance", () => {
  it("sends a refused key to the stream keys", () => {
    expect(describeResolveFailure("FORBIDDEN").actions).toContain("keys");
    expect(describeResolveFailure("NOT_FOUND").actions).toContain("keys");
  });

  it("sends an expired session to sign in", () => {
    expect(describeResolveFailure("UNAUTHORIZED").actions).toEqual(["login"]);
  });

  it("covers account refusals under FORBIDDEN, with keys and billing", () => {
    const failure = describeResolveFailure("FORBIDDEN");
    expect(failure.title).toBe("This stream or account cannot broadcast right now");
    expect(failure.actions).toEqual(["keys", "billing"]);
  });

  it("tells a duplicate ingest to stop the other encoder", () => {
    const failure = describeResolveFailure("CONFLICT");
    expect(failure.title).toMatch(/already receiving video from another encoder/);
    expect(failure.detail).toMatch(/Stop the other encoder/);
    expect(failure.actions).toEqual(["retry"]);
  });

  it("sends a spent balance to billing", () => {
    expect(describeResolveFailure("FAILED_PRECONDITION").actions).toEqual(["billing"]);
    expect(describeResolveFailure("FAILED_PRECONDITION").detail).toMatch(/below -€10/);
    expect(describeResolveFailure("PAYMENT_REQUIRED").actions).toEqual(["billing"]);
    expect(describeStudioFailure("WHIP request failed: 402 Payment Required - ").actions).toEqual([
      "billing",
    ]);
  });

  it("offers a retry when no node could be reached", () => {
    expect(describeResolveFailure("UNAVAILABLE").actions).toEqual(["retry"]);
    expect(describeResolveFailure("TIMEOUT").actions).toEqual(["retry"]);
  });

  it("explains camera permissions without asking for a reload", () => {
    const failure = describeStudioFailure("Permission request failed: NotAllowedError");
    expect(failure.title).toMatch(/blocked your camera/);
    expect(failure.detail).not.toMatch(/reload/i);
  });

  it("points a refused WHIP publish at keys and billing", () => {
    const failure = describeStudioFailure("WHIP request failed: 403 Forbidden - denied");
    expect(failure.actions).toEqual(["keys", "billing"]);
  });

  it("never repeats the raw studio message, which can carry the stream key", () => {
    const raw = "WHIP request failed: 500 - https://edge.example/webrtc/sk_secret";
    const failure = describeStudioFailure(raw);
    expect(`${failure.title} ${failure.detail}`).not.toContain("sk_secret");
  });
});

describe("Go Live studio settings", () => {
  function memoryStorage(): Storage {
    const values = new Map<string, string>();
    return {
      getItem: (k) => values.get(k) ?? null,
      setItem: (k, v) => void values.set(k, v),
      removeItem: (k) => void values.delete(k),
      clear: () => values.clear(),
      key: () => null,
      get length() {
        return values.size;
      },
    };
  }

  it("round-trips a saved choice", () => {
    const storage = memoryStorage();
    saveStudioSettings(storage, { enableCompositor: false, compositorRenderer: "canvas2d" });
    expect(loadStudioSettings(storage)).toEqual({
      enableCompositor: false,
      compositorRenderer: "canvas2d",
    });
  });

  it("falls back to the defaults when storage throws or holds junk", () => {
    const throwing = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
    } as unknown as Storage;
    expect(loadStudioSettings(throwing)).toEqual(DEFAULT_STUDIO_SETTINGS);
    expect(() => saveStudioSettings(throwing, DEFAULT_STUDIO_SETTINGS)).not.toThrow();
    const junk = memoryStorage();
    junk.setItem("fw.goLive.studioSettings", '{"compositorRenderer":"vulkan"}');
    expect(loadStudioSettings(junk)).toEqual(DEFAULT_STUDIO_SETTINGS);
    junk.setItem("fw.goLive.studioSettings", '{"compositorRenderer":"toString"}');
    expect(loadStudioSettings(junk)).toEqual(DEFAULT_STUDIO_SETTINGS);
  });
});
