import { describe, expect, it } from "vitest";
import { createMemorySessionStore, fileMatches, isExpired, SESSION_TTL_MS } from "./session-store";
import type { UploadSessionRecord } from "./types";

const sample = (override: Partial<UploadSessionRecord> = {}): UploadSessionRecord => ({
  uploadId: "u1",
  file: { name: "video.mp4", size: 1024, lastModified: 1, type: "video/mp4" },
  partSize: 512,
  totalParts: 2,
  parts: [
    { partNumber: 1, presignedUrl: "https://x/1", status: "completed", attempts: 1, etag: "abc" },
    { partNumber: 2, presignedUrl: "https://x/2", status: "pending", attempts: 0 },
  ],
  createdAt: 1_000,
  lastTouchedAt: 1_000,
  ...override,
});

describe("fileMatches", () => {
  it("matches on name+size+lastModified", () => {
    const a = { name: "v.mp4", size: 100, lastModified: 5, type: "video/mp4" };
    expect(fileMatches(a, { ...a })).toBe(true);
  });

  it("rejects different name/size/lastModified", () => {
    const a = { name: "v.mp4", size: 100, lastModified: 5, type: "video/mp4" };
    expect(fileMatches(a, { ...a, size: 101 })).toBe(false);
    expect(fileMatches(a, { ...a, name: "x.mp4" })).toBe(false);
    expect(fileMatches(a, { ...a, lastModified: 6 })).toBe(false);
  });
});

describe("isExpired", () => {
  it("expires past TTL", () => {
    const r = sample({ createdAt: 0 });
    expect(isExpired(r, SESSION_TTL_MS - 1)).toBe(false);
    expect(isExpired(r, SESSION_TTL_MS + 1)).toBe(true);
  });
});

describe("memory session store", () => {
  it("round-trips records", async () => {
    const store = createMemorySessionStore();
    await store.put(sample());
    const got = await store.get("u1");
    expect(got?.uploadId).toBe("u1");
    expect(got?.parts).toHaveLength(2);
  });

  it("returns undefined for missing", async () => {
    const store = createMemorySessionStore();
    expect(await store.get("missing")).toBeUndefined();
  });

  it("lists and deletes", async () => {
    const store = createMemorySessionStore();
    await store.put(sample({ uploadId: "u1" }));
    await store.put(sample({ uploadId: "u2" }));
    expect((await store.list()).map((r) => r.uploadId).sort()).toEqual(["u1", "u2"]);
    await store.delete("u1");
    expect((await store.list()).map((r) => r.uploadId)).toEqual(["u2"]);
  });
});
