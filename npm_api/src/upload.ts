import type { FrameWorksClient } from "./client.js";
import { type FrameWorksError, NetworkError, UploadError } from "./errors.js";
import {
  AbortVodUploadDocument,
  CompleteVodUploadDocument,
  CreateVodUploadDocument,
  type VodAssetFieldsFragment,
} from "./generated/graphql.js";
import { expectResult } from "./results.js";
import {
  backoffDelay,
  defaultRetryPolicy,
  defaultSleep,
  parseRetryAfter,
  retryableStatuses,
  type Sleep,
} from "./retry.js";

/** The file to upload. In Node, fs.openAsBlob(path) returns a Blob without reading the file into memory. */
export type UploadSource = Blob | ArrayBuffer | Uint8Array;

export interface UploadVodOptions {
  filename: string;
  contentType?: string;
  title?: string;
  description?: string;
  /** Parts sent at once (default 4). */
  concurrency?: number;
  /** Attempts per part, including the first (default 3). */
  partAttempts?: number;
  signal?: AbortSignal;
  /** Called after each part with the bytes uploaded so far. */
  onProgress?: (uploadedBytes: number, totalBytes: number) => void;
  /** fetch used for the part PUTs; defaults to the global fetch. */
  fetch?: typeof fetch;
}

/** Test hooks that are not part of the public API. */
export interface UploadInternals {
  sleep?: Sleep;
}

interface PartSource {
  size: number;
  slice(start: number, end: number): Blob | Uint8Array;
}

/** Replaces url, and its query, in text with the URL's origin and path. */
function redactUrl(text: string, url: string): string {
  let visible = "[presigned URL]";
  try {
    const parsed = new URL(url);
    visible = `${parsed.origin}${parsed.pathname}`;
  } catch {
    // An unparseable URL is hidden entirely.
  }
  const query = url.indexOf("?");
  let out = text.split(url).join(visible);
  if (query >= 0) {
    out = out.split(url.slice(query)).join("");
  }
  return out;
}

function toPartSource(source: UploadSource): PartSource {
  if (source instanceof Blob) {
    return { size: source.size, slice: (start, end) => source.slice(start, end) };
  }
  const bytes = source instanceof Uint8Array ? source : new Uint8Array(source);
  return { size: bytes.byteLength, slice: (start, end) => bytes.subarray(start, end) };
}

/**
 * Uploads a file as a VOD asset: createVodUpload, a PUT of every part to its
 * presigned URL (at most `concurrency` at once, each retried on network
 * errors, 408, 429, and 5xx, waiting a storage Retry-After up to the GraphQL
 * transport's 30-second maximum), then completeVodUpload with the part ETags.
 * Any failure after the upload is created aborts it with abortVodUpload
 * before the error is thrown.
 */
export async function uploadVod(
  client: FrameWorksClient,
  source: UploadSource,
  options: UploadVodOptions,
  internals: UploadInternals = {}
): Promise<VodAssetFieldsFragment> {
  const file = toPartSource(source);
  const sleep = internals.sleep ?? defaultSleep;
  const put = options.fetch ?? globalThis.fetch.bind(globalThis);
  const created = await client.request(
    CreateVodUploadDocument,
    {
      input: {
        filename: options.filename,
        sizeBytes: file.size,
        contentType: options.contentType,
        title: options.title,
        description: options.description,
      },
    },
    { signal: options.signal }
  );
  const session = expectResult(created.createVodUpload, "VodUploadSession");

  try {
    const parts = [...session.parts].sort((a, b) => a.partNumber - b.partNumber);
    const etags = new Map<number, string>();
    let uploaded = 0;
    let next = 0;
    const attempts = Math.max(1, options.partAttempts ?? 3);

    const sendPart = async (part: { partNumber: number; presignedUrl: string }) => {
      const start = (part.partNumber - 1) * session.partSize;
      const end = Math.min(start + session.partSize, file.size);
      for (let attempt = 1; ; attempt++) {
        let failure: FrameWorksError;
        let retryAfter: number | null = null;
        try {
          const res = await put(part.presignedUrl, {
            method: "PUT",
            body: file.slice(start, end) as BodyInit,
            signal: options.signal,
          });
          await res.arrayBuffer().catch(() => undefined);
          const etag = res.headers.get("etag");
          if (res.ok && etag) {
            etags.set(part.partNumber, etag);
            uploaded += end - start;
            options.onProgress?.(uploaded, file.size);
            return;
          }
          failure = new UploadError(
            res.ok
              ? `part ${part.partNumber} returned no ETag`
              : `part ${part.partNumber} failed with HTTP ${res.status}`,
            session.id,
            part.partNumber,
            { status: res.status }
          );
          if (!retryableStatuses.has(res.status)) {
            throw failure;
          }
          retryAfter = parseRetryAfter(res.headers.get("retry-after"));
        } catch (err) {
          if (err instanceof UploadError || options.signal?.aborted) {
            throw err;
          }
          // A fetch error can quote the URL, and a presigned URL's query is
          // a credential, so the error carries the redacted description only.
          const description = redactUrl(String(err), part.presignedUrl);
          failure = new UploadError(
            `part ${part.partNumber} failed: ${description}`,
            session.id,
            part.partNumber,
            { cause: new NetworkError(description) }
          );
        }
        // Retry-After above the GraphQL transport's maximum ends retrying
        // instead of waiting, as it does for GraphQL requests.
        if (
          attempt >= attempts ||
          (retryAfter !== null && retryAfter * 1000 > defaultRetryPolicy.maxRetryAfterMs)
        ) {
          throw failure;
        }
        await sleep(
          retryAfter !== null ? retryAfter * 1000 : backoffDelay(defaultRetryPolicy, attempt),
          options.signal
        );
      }
    };

    // After the first failed part no worker starts another one.
    let stopped = false;
    const worker = async () => {
      while (!stopped && next < parts.length) {
        const part = parts[next++];
        if (!part) {
          return;
        }
        try {
          await sendPart(part);
        } catch (err) {
          stopped = true;
          throw err;
        }
      }
    };
    const workers = Array.from(
      { length: Math.max(1, Math.min(options.concurrency ?? 4, parts.length)) },
      worker
    );
    const results = await Promise.allSettled(workers);
    const failed = results.find((r): r is PromiseRejectedResult => r.status === "rejected");
    if (failed) {
      throw failed.reason;
    }

    const completed = await client.request(
      CompleteVodUploadDocument,
      {
        input: {
          uploadId: session.id,
          parts: parts.map((p) => ({
            partNumber: p.partNumber,
            etag: etags.get(p.partNumber) ?? "",
          })),
        },
      },
      { signal: options.signal }
    );
    return expectResult(completed.completeVodUpload, "VodAsset");
  } catch (err) {
    await client.request(AbortVodUploadDocument, { uploadId: session.id }).catch(() => undefined);
    throw err;
  }
}
