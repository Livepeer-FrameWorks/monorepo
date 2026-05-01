import { describe, expect, it } from "vitest";
import { findRecoverable } from "./recovery";
import { createMemorySessionStore, SESSION_TTL_MS } from "./session-store";
import type { UploadSessionRecord } from "./types";

function makeFile(name: string, size: number, lastModified: number, type = "video/mp4"): File {
  return new File([new Uint8Array(size)], name, { lastModified, type });
}

function record(over: Partial<UploadSessionRecord> = {}): UploadSessionRecord {
  return {
    uploadId: "u",
    file: { name: "v.mp4", size: 100, lastModified: 1, type: "video/mp4" },
    partSize: 50,
    totalParts: 2,
    parts: [
      { partNumber: 1, presignedUrl: "u1", status: "completed", attempts: 1, etag: "et1" },
      { partNumber: 2, presignedUrl: "u2", status: "pending", attempts: 0 },
    ],
    createdAt: 1_000,
    lastTouchedAt: 1_000,
    ...over,
  };
}

describe("findRecoverable", () => {
  it("returns null when no sessions exist", async () => {
    const store = createMemorySessionStore();
    const got = await findRecoverable(store, makeFile("v.mp4", 100, 1));
    expect(got).toBeNull();
  });

  it("matches by file identity and returns completed parts", async () => {
    const store = createMemorySessionStore();
    await store.put(record({ uploadId: "match" }));
    const got = await findRecoverable(store, makeFile("v.mp4", 100, 1), 2_000);
    expect(got?.record.uploadId).toBe("match");
    expect(got?.completedParts).toEqual([{ partNumber: 1, etag: "et1" }]);
  });

  it("rejects sessions whose file identity does not match", async () => {
    const store = createMemorySessionStore();
    await store.put(record({ uploadId: "match" }));
    const got = await findRecoverable(store, makeFile("other.mp4", 100, 1), 2_000);
    expect(got).toBeNull();
  });

  it("drops expired sessions and returns null when only expired matches exist", async () => {
    const store = createMemorySessionStore();
    await store.put(record({ uploadId: "old", createdAt: 0 }));
    const now = SESSION_TTL_MS + 5_000;
    const got = await findRecoverable(store, makeFile("v.mp4", 100, 1), now);
    expect(got).toBeNull();
    expect(await store.get("old")).toBeUndefined();
  });

  it("prefers the most recently touched matching session", async () => {
    const store = createMemorySessionStore();
    await store.put(record({ uploadId: "older", lastTouchedAt: 1_000 }));
    await store.put(record({ uploadId: "newer", lastTouchedAt: 2_000 }));
    const got = await findRecoverable(store, makeFile("v.mp4", 100, 1), 3_000);
    expect(got?.record.uploadId).toBe("newer");
  });
});
