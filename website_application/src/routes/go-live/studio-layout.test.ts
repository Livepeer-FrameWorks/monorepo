import { describe, expect, it } from "vitest";
import type { StudioLayout } from "@livepeer-frameworks/streamcrafter-core";
import { loadStudioLayout, saveStudioLayout, studioTarget } from "./studio-layout";

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

function layout(name: string): StudioLayout {
  return {
    version: 1,
    layout: { mode: "solo" },
    scenes: [{ name, backgroundColor: "#000000" }],
    activeScene: 0,
    overlays: [],
  } as unknown as StudioLayout;
}

describe("Go Live studio layout per stream", () => {
  it("keeps a separate layout for each stream", () => {
    const storage = memoryStorage();
    saveStudioLayout(storage, "stream-a", layout("A"));
    saveStudioLayout(storage, "stream-b", layout("B"));
    expect(loadStudioLayout(storage, "stream-a")).toEqual(layout("A"));
    expect(loadStudioLayout(storage, "stream-b")).toEqual(layout("B"));
    expect(loadStudioLayout(storage, "stream-c")).toBeNull();
  });

  it("starts from the default layout when storage is blocked or holds junk", () => {
    const blocked = {
      getItem: () => {
        throw new Error("SecurityError");
      },
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
    } as unknown as Storage;
    expect(() => saveStudioLayout(blocked, "stream-a", layout("A"))).not.toThrow();
    expect(loadStudioLayout(blocked, "stream-a")).toBeNull();
    expect(loadStudioLayout(undefined, "stream-a")).toBeNull();

    const storage = memoryStorage();
    storage.setItem("fw.goLive.studioLayout.stream-a", "{not json");
    expect(loadStudioLayout(storage, "stream-a")).toBeNull();
    storage.setItem("fw.goLive.studioLayout.stream-a", "[1,2]");
    expect(loadStudioLayout(storage, "stream-a")).toBeNull();
  });

  it("keeps the studio's URL and layout object for the same stream", () => {
    const current = { streamId: "stream-a", whipUrl: "https://edge/a", layout: layout("A") };
    const next = studioTarget(current, "stream-a", "https://other-edge/a", () => layout("X"));
    expect(next).toBe(current);
  });

  it("switches URL and saved layout together for another stream", () => {
    const storage = memoryStorage();
    saveStudioLayout(storage, "stream-b", layout("B"));
    const current = { streamId: "stream-a", whipUrl: "https://edge/a", layout: layout("A") };
    const next = studioTarget(current, "stream-b", "https://edge/b", (id) =>
      loadStudioLayout(storage, id)
    );
    expect(next.streamId).toBe("stream-b");
    expect(next.whipUrl).toBe("https://edge/b");
    expect(next.layout).toEqual(layout("B"));
    expect(next.layout).not.toBe(current.layout);
  });

  it("opens the first stream with its saved layout", () => {
    const storage = memoryStorage();
    saveStudioLayout(storage, "stream-a", layout("A"));
    const next = studioTarget(
      { streamId: "", whipUrl: null, layout: null },
      "stream-a",
      "u",
      (id) => loadStudioLayout(storage, id)
    );
    expect(next).toEqual({ streamId: "stream-a", whipUrl: "u", layout: layout("A") });
  });
});
