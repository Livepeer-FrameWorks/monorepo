import { describe, expect, it } from "vitest";
import { createUploadEngine } from "./engine";
import { createMemorySessionStore } from "./session-store";
import type { CompletedPart, EngineEvent, PartDescriptor } from "./types";

function file(size: number, name = "video.mp4"): File {
  return new File([new Uint8Array(size)], name, {
    lastModified: 1,
    type: "video/mp4",
  });
}

function descriptors(n: number): PartDescriptor[] {
  return Array.from({ length: n }, (_, i) => ({
    partNumber: i + 1,
    presignedUrl: `https://s3.example/part/${i + 1}`,
  }));
}

function okResponse(etag: string): Response {
  return new Response("", { status: 200, headers: { ETag: `"${etag}"` } });
}

function failResponse(status: number): Response {
  return new Response("err", { status });
}

const noDelay = () => Promise.resolve();

describe("createUploadEngine", () => {
  it("uploads all parts and emits transferComplete", async () => {
    const f = file(300);
    const events: EngineEvent[] = [];
    const store = createMemorySessionStore();
    const fetchImpl = async (url: RequestInfo | URL): Promise<Response> => {
      const num = Number(String(url).split("/").pop());
      return okResponse(`etag-${num}`);
    };
    const engine = createUploadEngine({
      uploadId: "u",
      file: f,
      partSize: 100,
      parts: descriptors(3),
      concurrency: 2,
      store,
      fetchImpl: fetchImpl as typeof fetch,
      delay: noDelay,
    });
    engine.on((e) => events.push(e));
    engine.start();

    await new Promise<void>((resolve) => {
      engine.on((e) => {
        if (e.type === "stateChange" && e.state === "completed") resolve();
      });
    });

    const transfer = events.find((e) => e.type === "transferComplete");
    expect(transfer).toBeTruthy();
    expect((transfer as { parts: CompletedPart[] }).parts).toEqual([
      { partNumber: 1, etag: "etag-1" },
      { partNumber: 2, etag: "etag-2" },
      { partNumber: 3, etag: "etag-3" },
    ]);
    const lastProgress = [...events].reverse().find((e) => e.type === "progress");
    expect(lastProgress && (lastProgress as { percent: number }).percent).toBe(100);
    await expect(store.get("u")).resolves.toMatchObject({
      uploadId: "u",
      parts: [
        expect.objectContaining({ partNumber: 1, status: "completed", etag: "etag-1" }),
        expect.objectContaining({ partNumber: 2, status: "completed", etag: "etag-2" }),
        expect.objectContaining({ partNumber: 3, status: "completed", etag: "etag-3" }),
      ],
    });
  });

  it("retries 5xx then succeeds without aborting whole upload", async () => {
    const f = file(200);
    let calls = 0;
    const fetchImpl = async (url: RequestInfo | URL): Promise<Response> => {
      const num = Number(String(url).split("/").pop());
      if (num === 1 && calls++ < 1) return failResponse(503);
      return okResponse(`et-${num}`);
    };
    const engine = createUploadEngine({
      uploadId: "u",
      file: f,
      partSize: 100,
      parts: descriptors(2),
      store: createMemorySessionStore(),
      fetchImpl: fetchImpl as typeof fetch,
      delay: noDelay,
    });
    const events: EngineEvent[] = [];
    engine.on((e) => events.push(e));
    engine.start();
    await new Promise<void>((resolve) => {
      engine.on((e) => {
        if (e.type === "stateChange" && e.state === "completed") resolve();
      });
    });
    const failures = events.filter((e) => e.type === "partFailed");
    expect(failures.length).toBeGreaterThan(0);
    expect(engine.completedParts()).toEqual([
      { partNumber: 1, etag: "et-1" },
      { partNumber: 2, etag: "et-2" },
    ]);
  });

  it("fails fast on 403 (expired presigned URL)", async () => {
    const f = file(100);
    const fetchImpl = async (): Promise<Response> => failResponse(403);
    const engine = createUploadEngine({
      uploadId: "u",
      file: f,
      partSize: 100,
      parts: descriptors(1),
      store: createMemorySessionStore(),
      fetchImpl: fetchImpl as typeof fetch,
      delay: noDelay,
    });
    const events: EngineEvent[] = [];
    engine.on((e) => events.push(e));
    engine.start();
    await new Promise<void>((resolve) => {
      engine.on((e) => {
        if (e.type === "stateChange" && e.state === "failed") resolve();
      });
    });
    const failures = events.filter((e) => e.type === "partFailed");
    // Expect exactly one attempt — no retry on 403.
    expect(failures.length).toBe(1);
  });

  it("seedCompleted skips already-uploaded parts on resume", async () => {
    const f = file(300);
    const requested: number[] = [];
    const fetchImpl = async (url: RequestInfo | URL): Promise<Response> => {
      const num = Number(String(url).split("/").pop());
      requested.push(num);
      return okResponse(`et-${num}`);
    };
    const engine = createUploadEngine({
      uploadId: "u",
      file: f,
      partSize: 100,
      parts: descriptors(3),
      store: createMemorySessionStore(),
      fetchImpl: fetchImpl as typeof fetch,
      delay: noDelay,
    });
    engine.seedCompleted([
      { partNumber: 1, etag: "et-1" },
      { partNumber: 2, etag: "et-2" },
    ]);
    engine.start();
    await new Promise<void>((resolve) => {
      engine.on((e) => {
        if (e.type === "stateChange" && e.state === "completed") resolve();
      });
    });
    expect(requested).toEqual([3]);
    expect(engine.completedParts()).toEqual([
      { partNumber: 1, etag: "et-1" },
      { partNumber: 2, etag: "et-2" },
      { partNumber: 3, etag: "et-3" },
    ]);
  });
});
