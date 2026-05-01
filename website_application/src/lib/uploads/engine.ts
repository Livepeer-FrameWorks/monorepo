import { sliceFilePart, partByteRange } from "./chunker";
import { backoffDelayMs, MAX_ATTEMPTS, shouldRetry } from "./retry";
import { fileIdentityOf, type SessionStore, createMemorySessionStore } from "./session-store";
import type {
  CompletedPart,
  EngineEvent,
  EngineListener,
  EngineState,
  PartDescriptor,
  PartState,
  UploadSessionRecord,
} from "./types";
import { WorkerPool } from "./worker-pool";

export interface UploadEngineConfig {
  uploadId: string;
  file: File;
  partSize: number;
  parts: PartDescriptor[];
  concurrency?: number;
  store?: SessionStore;
  fetchImpl?: typeof fetch;
  delay?: (ms: number) => Promise<void>;
}

const DEFAULT_CONCURRENCY = 4;

export interface UploadEngine {
  readonly uploadId: string;
  state(): EngineState;
  start(): void;
  pause(): void;
  resume(): void;
  abort(): void;
  on(listener: EngineListener): () => void;
  completedParts(): CompletedPart[];
  /** Start with these part numbers already considered complete (server-reconciled resume). */
  seedCompleted(parts: CompletedPart[]): void;
}

export function createUploadEngine(cfg: UploadEngineConfig): UploadEngine {
  const concurrency = cfg.concurrency ?? DEFAULT_CONCURRENCY;
  const store = cfg.store ?? createMemorySessionStore();
  const fetchImpl = cfg.fetchImpl ?? fetch;
  const sleep = cfg.delay ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));

  const partsState: PartState[] = cfg.parts.map((p) => ({
    partNumber: p.partNumber,
    presignedUrl: p.presignedUrl,
    status: "pending",
    attempts: 0,
  }));
  const partsByNumber = new Map<number, PartState>();
  for (const ps of partsState) partsByNumber.set(ps.partNumber, ps);

  const totalBytes = cfg.file.size;
  let uploadedBytes = 0;
  let state: EngineState = "idle";
  const listeners = new Set<EngineListener>();

  const emit = (e: EngineEvent) => {
    for (const l of listeners) l(e);
  };
  const setState = (s: EngineState) => {
    if (state === s) return;
    state = s;
    emit({ type: "stateChange", state });
  };

  const persist = async () => {
    const record: UploadSessionRecord = {
      uploadId: cfg.uploadId,
      file: fileIdentityOf(cfg.file),
      partSize: cfg.partSize,
      totalParts: cfg.parts.length,
      parts: partsState.map((p) => ({
        partNumber: p.partNumber,
        presignedUrl: p.presignedUrl,
        status: p.status,
        etag: p.etag,
        attempts: p.attempts,
        lastError: p.lastError,
      })),
      createdAt: Date.now(),
      lastTouchedAt: Date.now(),
    };
    try {
      await store.put(record);
    } catch {
      // Persistence is best effort; the in-memory upload continues, but reload recovery may be unavailable.
    }
  };

  const pool = new WorkerPool<PartState, void>({
    concurrency,
    handler: async (item, signal) => {
      await uploadPart(item.payload, signal);
    },
    events: {
      onItemError: (_item, err) => {
        emit({ type: "error", error: err instanceof Error ? err.message : String(err) });
      },
    },
  });

  async function uploadPart(part: PartState, signal: AbortSignal): Promise<void> {
    const range = partByteRange(part.partNumber, cfg.partSize, totalBytes);
    const blob = sliceFilePart(cfg.file, part.partNumber, cfg.partSize);

    while (true) {
      if (signal.aborted) {
        part.status = "pending";
        return;
      }
      part.attempts += 1;
      part.status = "in_flight";
      try {
        const res = await fetchImpl(part.presignedUrl, {
          method: "PUT",
          body: blob,
          headers: { "Content-Type": cfg.file.type || "application/octet-stream" },
          signal,
        });
        if (!res.ok) {
          const err = Object.assign(new Error(`part ${part.partNumber} HTTP ${res.status}`), {
            status: res.status,
          });
          throw err;
        }
        const etag = (res.headers.get("ETag") ?? "").replace(/"/g, "");
        if (!etag) {
          throw Object.assign(new Error(`part ${part.partNumber} missing ETag`), { status: 500 });
        }
        part.etag = etag;
        part.status = "completed";
        part.lastError = undefined;
        uploadedBytes += range.size;
        emit({ type: "partCompleted", partNumber: part.partNumber, etag });
        emit({
          type: "progress",
          uploadedBytes,
          totalBytes,
          percent: totalBytes === 0 ? 100 : Math.round((uploadedBytes / totalBytes) * 100),
        });
        await persist();
        return;
      } catch (err) {
        if (signal.aborted) {
          part.status = "pending";
          return;
        }
        const re = err as { status?: number; name?: string; message?: string };
        const msg = re.message ?? String(err);
        part.lastError = msg;
        const willRetry = shouldRetry(re, part.attempts);
        emit({
          type: "partFailed",
          partNumber: part.partNumber,
          error: msg,
          attempt: part.attempts,
          willRetry,
        });
        if (!willRetry) {
          part.status = "failed";
          throw err;
        }
        const delay = backoffDelayMs(part.attempts);
        await sleep(delay);
      }
      if (part.attempts >= MAX_ATTEMPTS) {
        part.status = "failed";
        throw Object.assign(new Error(`part ${part.partNumber} exhausted retries`), {
          status: 0,
        });
      }
    }
  }

  function pendingItems() {
    return partsState
      .filter((p) => p.status !== "completed")
      .map((p) => ({ id: `part-${p.partNumber}`, payload: p }));
  }

  async function runToCompletion() {
    setState("uploading");
    pool.enqueue(pendingItems());
    pool.start();
    await pool.drain();

    const failed = partsState.find((p) => p.status === "failed");
    if (failed) {
      setState("failed");
      emit({
        type: "error",
        error: `part ${failed.partNumber} failed: ${failed.lastError ?? "unknown"}`,
      });
      return;
    }
    if (state === "paused" || state === "aborted") return;

    setState("completing");
    const completed: CompletedPart[] = partsState
      .filter((p) => p.status === "completed" && p.etag)
      .sort((a, b) => a.partNumber - b.partNumber)
      .map((p) => ({ partNumber: p.partNumber, etag: p.etag! }));
    emit({ type: "transferComplete", parts: completed });
    setState("completed");
  }

  return {
    uploadId: cfg.uploadId,
    state: () => state,
    start() {
      if (state === "uploading" || state === "completed") return;
      if (state === "paused") {
        setState("uploading");
        pool.resume();
        return;
      }
      // Account for already-completed parts (resume).
      uploadedBytes = partsState
        .filter((p) => p.status === "completed")
        .reduce((acc, p) => acc + partByteRange(p.partNumber, cfg.partSize, totalBytes).size, 0);
      void runToCompletion();
    },
    pause() {
      if (state !== "uploading") return;
      pool.pause();
      setState("paused");
    },
    resume() {
      if (state !== "paused") return;
      setState("uploading");
      pool.resume();
    },
    abort() {
      pool.abort();
      setState("aborted");
    },
    on(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    completedParts() {
      return partsState
        .filter((p) => p.status === "completed" && p.etag)
        .sort((a, b) => a.partNumber - b.partNumber)
        .map((p) => ({ partNumber: p.partNumber, etag: p.etag! }));
    },
    seedCompleted(parts) {
      for (const p of parts) {
        const ps = partsByNumber.get(p.partNumber);
        if (!ps) continue;
        ps.status = "completed";
        ps.etag = p.etag;
      }
    },
  };
}
