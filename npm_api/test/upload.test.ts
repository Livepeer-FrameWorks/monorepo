import { createServer, type IncomingMessage } from "node:http";
import type { AddressInfo } from "node:net";

import { describe, expect, it } from "vitest";

import { createClientWith } from "../src/client.js";
import { uploadVod } from "../src/upload.js";
import { errorMismatches, type ExpectedError, loadFixture } from "./fixtures.js";

interface UploadFixture {
  presignedSignature: string;
  cases: Array<{
    name: string;
    sizeBytes: number;
    partSize: number;
    concurrency: number;
    partAttempts: number;
    putFailures?: Record<string, number[]>;
    putRetryAfter?: string;
    putDrops?: Record<string, number>;
    createResult?: Record<string, unknown>;
    completeResult?: Record<string, unknown>;
    expect: {
      ok: boolean;
      error?: ExpectedError;
      errorOmits?: string;
      puts?: Record<string, number>;
      partBytes?: Record<string, number>;
      completedParts?: Array<{ partNumber: number; etag: string }>;
      aborted: boolean;
      completed?: boolean;
      maxConcurrentPuts?: number;
    };
  }>;
}

const fixture = loadFixture<UploadFixture>("upload.json");

/** The messages of an error and of every error it wraps. */
function errorText(err: unknown): string {
  const parts: string[] = [];
  for (let e: unknown = err; e instanceof Error; e = e.cause) {
    parts.push(e.message);
  }
  return parts.join("\n");
}

function readBody(req: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => resolve(Buffer.concat(chunks)));
  });
}

const vodAsset = {
  __typename: "VodAsset",
  id: "vod-1",
  artifactHash: "hash",
  playbackId: "play",
  streamId: null,
  title: null,
  description: null,
  filename: "file.mp4",
  status: "PROCESSING",
  sizeBytes: 10,
  durationMs: null,
  resolution: null,
  videoCodec: null,
  audioCodec: null,
  bitrateKbps: null,
  createdAt: "2026-09-19T14:03:27Z",
  updatedAt: "2026-09-19T14:03:27Z",
  expiresAt: null,
  errorMessage: null,
  playbackPolicy: null,
  thumbnailAssets: null,
  effectiveRetention: null,
};

describe("VOD upload helper (sdk_conformance/upload.json)", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      const partCount = Math.ceil(tc.sizeBytes / tc.partSize);
      const puts: Record<string, number> = {};
      const partBytes: Record<string, number> = {};
      const failures = Object.fromEntries(
        Object.entries(tc.putFailures ?? {}).map(([k, v]) => [k, [...v]])
      );
      const drops: Record<string, number> = { ...tc.putDrops };
      let inFlight = 0;
      let maxInFlight = 0;
      let aborted = false;
      let completed = false;
      let completedParts: unknown = null;
      let fileOK = true;

      const server = createServer(async (req, res) => {
        const body = await readBody(req);
        const url = new URL(req.url ?? "/", "http://localhost");
        if (req.method === "PUT") {
          const part = url.searchParams.get("part")!;
          puts[part] = (puts[part] ?? 0) + 1;
          inFlight++;
          maxInFlight = Math.max(maxInFlight, inFlight);
          await new Promise((r) => setTimeout(r, 5));
          inFlight--;
          const dropsLeft = drops[part] ?? 0;
          if (dropsLeft > 0) {
            drops[part] = dropsLeft - 1;
            req.socket.destroy();
            return;
          }
          const failure = failures[part]?.shift();
          if (failure) {
            res
              .writeHead(failure, tc.putRetryAfter ? { "Retry-After": tc.putRetryAfter } : {})
              .end();
            return;
          }
          const offset = (Number(part) - 1) * tc.partSize;
          body.forEach((b, i) => {
            if (b !== (offset + i) % 251) {
              fileOK = false;
            }
          });
          partBytes[part] = body.length;
          res.writeHead(200, { ETag: `"etag-${part}"` }).end();
          return;
        }
        const request = JSON.parse(body.toString()) as {
          operationName: string;
          variables: Record<string, unknown>;
        };
        const origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
        let data: unknown;
        switch (request.operationName) {
          case "CreateVodUpload":
            data = {
              createVodUpload: tc.createResult ?? {
                __typename: "VodUploadSession",
                id: "upload-1",
                artifactId: "artifact-1",
                artifactHash: "hash",
                playbackId: "play",
                partSize: tc.partSize,
                parts: Array.from({ length: partCount }, (_, i) => ({
                  partNumber: i + 1,
                  presignedUrl: `${origin}/s3?part=${i + 1}&X-Amz-Signature=${fixture.presignedSignature}`,
                })),
                expiresAt: "2026-09-20T14:03:27Z",
              },
            };
            break;
          case "CompleteVodUpload":
            completed = true;
            completedParts = (request.variables.input as { parts: unknown }).parts;
            data = { completeVodUpload: tc.completeResult ?? vodAsset };
            break;
          case "AbortVodUpload":
            aborted = true;
            data = {
              abortVodUpload: {
                __typename: "DeleteSuccess",
                success: true,
                deletedId: "upload-1",
                pending: null,
              },
            };
            break;
        }
        res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ data }));
      });
      await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
      try {
        const port = (server.address() as AddressInfo).port;
        const client = createClientWith(
          { url: `http://127.0.0.1:${port}/graphql`, token: "t", checkServer: false },
          {}
        );
        const file = Uint8Array.from({ length: tc.sizeBytes }, (_, i) => i % 251);
        const outcome = await uploadVod(
          client,
          file,
          { filename: "file.mp4", concurrency: tc.concurrency, partAttempts: tc.partAttempts },
          { sleep: async () => undefined }
        ).then(
          (asset) => ({ asset }),
          (err: unknown) => ({ err })
        );
        if (tc.expect.ok) {
          expect(outcome).toEqual({ asset: vodAsset });
        } else {
          expect("err" in outcome).toBe(true);
          expect(errorMismatches((outcome as { err: unknown }).err, tc.expect.error!)).toEqual([]);
          if (tc.expect.errorOmits) {
            expect(errorText((outcome as { err: unknown }).err)).not.toContain(
              tc.expect.errorOmits
            );
          }
        }
        if (tc.expect.puts) {
          expect(puts).toEqual(tc.expect.puts);
        }
        if (tc.expect.partBytes) {
          expect(partBytes).toEqual(tc.expect.partBytes);
        }
        if (tc.expect.completedParts) {
          expect(completedParts).toEqual(tc.expect.completedParts);
        }
        if (tc.expect.completed !== undefined) {
          expect(completed).toBe(tc.expect.completed);
        }
        if (tc.expect.maxConcurrentPuts !== undefined) {
          expect(maxInFlight).toBeLessThanOrEqual(tc.expect.maxConcurrentPuts);
        }
        expect(aborted).toBe(tc.expect.aborted);
        expect(fileOK).toBe(true);
      } finally {
        await new Promise((resolve) => server.close(resolve));
      }
    });
  }
});

describe("VOD upload errors", () => {
  it("never quote the presigned URL's query, even when fetch does", async () => {
    const presignedUrl = `https://storage.test/bucket/part-1?X-Amz-Signature=${fixture.presignedSignature}`;
    const graphql = async (_url: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const { operationName } = JSON.parse(String(init?.body)) as { operationName: string };
      const data =
        operationName === "CreateVodUpload"
          ? {
              createVodUpload: {
                __typename: "VodUploadSession",
                id: "upload-1",
                artifactId: "artifact-1",
                artifactHash: "hash",
                playbackId: "play",
                partSize: 4,
                parts: [{ partNumber: 1, presignedUrl }],
                expiresAt: "2026-09-20T14:03:27Z",
              },
            }
          : {
              abortVodUpload: {
                __typename: "DeleteSuccess",
                success: true,
                deletedId: "upload-1",
                pending: null,
              },
            };
      return new Response(JSON.stringify({ data }), {
        headers: { "content-type": "application/json" },
      });
    };
    const client = createClientWith(
      { url: "https://api.test/graphql", fetch: graphql, token: "t", checkServer: false },
      {}
    );
    // Node's fetch quotes the whole URL when it cannot use it.
    const put = async (url: RequestInfo | URL): Promise<Response> => {
      throw new TypeError(`Failed to parse URL from ${String(url)}`);
    };
    const err = await uploadVod(
      client,
      new Uint8Array(4),
      { filename: "file.mp4", partAttempts: 1, fetch: put },
      { sleep: async () => undefined }
    ).catch((e: unknown) => e);
    expect((err as Error).name).toBe("UploadError");
    expect(errorText(err)).not.toContain(fixture.presignedSignature);
    expect(errorText(err)).toContain("https://storage.test/bucket/part-1");
  });
});
