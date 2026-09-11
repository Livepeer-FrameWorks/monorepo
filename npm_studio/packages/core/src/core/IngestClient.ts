/**
 * IngestClient - Resolves ingest endpoints via Gateway
 * Mirrors GatewayClient from npm_player for consistency
 */

import { TypedEventEmitter } from "./EventEmitter";
import type { IngestClientConfig, IngestClientEvents, IngestEndpoints } from "../types";

const RESOLVE_INGEST_QUERY = `
  query ResolveIngest($streamKey: String!) {
    resolveIngestEndpoint(streamKey: $streamKey, protocol: WHIP) {
      primary {
        nodeId
        baseUrl
        whipUrl
        rtmpUrl
        srtUrl
        region
        loadScore
      }
      fallbacks {
        nodeId
        baseUrl
        whipUrl
        rtmpUrl
        srtUrl
        region
        loadScore
      }
      metadata {
        streamId
        streamKey
        tenantId
        recordingEnabled
      }
    }
  }
`;

export class IngestClient extends TypedEventEmitter<IngestClientEvents> {
  private config: IngestClientConfig;
  private abortController: AbortController | null = null;
  private endpoints: IngestEndpoints | null = null;
  private destroyed = false;

  constructor(config: IngestClientConfig) {
    super();
    this.config = {
      maxRetries: 3,
      initialDelayMs: 1000,
      ...config,
    };
  }

  /**
   * Get currently resolved endpoints (null if not resolved yet)
   */
  getEndpoints(): IngestEndpoints | null {
    return this.endpoints;
  }

  /**
   * Resolve ingest endpoints from the gateway
   */
  async resolve(): Promise<IngestEndpoints> {
    const { gatewayUrl, streamKey, authToken, maxRetries, initialDelayMs } = this.config;
    if (this.destroyed) throw new DOMException("Resolution cancelled", "AbortError");
    this.abortController?.abort();
    const request = new AbortController();
    this.abortController = request;
    this.endpoints = null;
    this.emit("statusChange", { status: "loading" });
    let timedOut = false;
    const deadline = setTimeout(() => {
      timedOut = true;
      request.abort();
    }, 5000);
    const retryLimit =
      typeof maxRetries === "number" && Number.isFinite(maxRetries)
        ? Math.min(8, Math.max(0, Math.floor(maxRetries)))
        : 3;
    try {
      for (let attempt = 0; ; attempt++) {
        request.signal.throwIfAborted();
        try {
          const response = await abortable(
            fetch(gatewayUrl, {
              method: "POST",
              headers: {
                "Content-Type": "application/json",
                ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
              },
              body: JSON.stringify({ query: RESOLVE_INGEST_QUERY, variables: { streamKey } }),
              signal: request.signal,
            }),
            request.signal
          );
          if (!response.ok) throw new Error("Ingest gateway unavailable");
          const payload = await abortable(response.json(), request.signal);
          request.signal.throwIfAborted();
          const data = payload.data?.resolveIngestEndpoint;
          if (payload.errors?.length || !data || !validWhipEndpoint(data.primary)) {
            throw new Error("No valid WHIP destination was confirmed");
          }
          this.endpoints = {
            primary: data.primary,
            fallbacks: Array.isArray(data.fallbacks)
              ? data.fallbacks.filter(validWhipEndpoint)
              : [],
            metadata: data.metadata,
          };
          this.emit("endpointsResolved", { endpoints: this.endpoints });
          request.signal.throwIfAborted();
          this.emit("statusChange", { status: "ready" });
          request.signal.throwIfAborted();
          return this.endpoints;
        } catch {
          request.signal.throwIfAborted();
          if (attempt >= retryLimit) {
            throw new Error(
              "Failed to resolve ingest endpoint: No valid WHIP destination was confirmed"
            );
          }
          this.emit("statusChange", {
            status: "loading",
            error: `Retrying (${attempt + 1}/${retryLimit})...`,
          });
          const delay =
            typeof initialDelayMs === "number" && Number.isFinite(initialDelayMs)
              ? Math.max(0, initialDelayMs)
              : 1000;
          await retryDelay(Math.min(5000, delay * Math.pow(2, attempt)), request.signal);
        }
      }
    } catch (error) {
      if (this.abortController === request && !this.destroyed) {
        if (request.signal.aborted && !timedOut) {
          this.emit("statusChange", { status: "idle" });
        } else {
          this.emit("statusChange", {
            status: "error",
            error: timedOut
              ? "Ingest resolution timed out"
              : "No valid WHIP destination was confirmed",
          });
        }
      }
      if (timedOut) throw new Error("Failed to resolve ingest endpoint: resolution timed out");
      throw error;
    } finally {
      clearTimeout(deadline);
      if (this.abortController === request) this.abortController = null;
    }
  }

  /**
   * Get the primary WHIP URL for streaming
   */
  getWhipUrl(): string | null {
    return this.endpoints?.primary?.whipUrl || null;
  }

  /**
   * Get the primary RTMP URL for streaming
   */
  getRtmpUrl(): string | null {
    return this.endpoints?.primary?.rtmpUrl || null;
  }

  /**
   * Get the primary SRT URL for streaming
   */
  getSrtUrl(): string | null {
    return this.endpoints?.primary?.srtUrl || null;
  }

  /**
   * Clean up resources
   */
  destroy(): void {
    this.destroyed = true;
    this.abortController?.abort();
    this.abortController = null;
    this.endpoints = null;
    this.removeAllListeners();
  }
}

async function abortable<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  let cancel = () => {};
  const aborted = new Promise<never>((_resolve, reject) => {
    cancel = () => reject(new DOMException("Resolution cancelled", "AbortError"));
    signal.addEventListener("abort", cancel, { once: true });
    if (signal.aborted) cancel();
  });
  try {
    return await Promise.race([work, aborted]);
  } finally {
    signal.removeEventListener("abort", cancel);
  }
}

async function retryDelay(delay: number, signal: AbortSignal): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await abortable(
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, delay);
      }),
      signal
    );
  } finally {
    clearTimeout(timer);
  }
}

function validWhipEndpoint(endpoint: IngestEndpoints["primary"] | undefined): boolean {
  if (
    !endpoint ||
    typeof endpoint.nodeId !== "string" ||
    !endpoint.nodeId.trim() ||
    typeof endpoint.whipUrl !== "string" ||
    !endpoint.whipUrl
  )
    return false;
  try {
    const url = new URL(endpoint.whipUrl);
    return (
      ["https:", "http:"].includes(url.protocol) &&
      !!url.hostname &&
      !url.username &&
      !url.password &&
      !url.hash
    );
  } catch {
    return false;
  }
}
