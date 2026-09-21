/**
 * The serverInfo gate for code that talks to a gateway without a
 * FrameWorksClient, such as the player and StreamCrafter resolving endpoints
 * anonymously: one probe per gateway URL shared by every caller on the page,
 * the client's version rules, and the same cache and re-ask behaviour.
 */

import { PaymentRequiredError, ServerTooOldError } from "./errors.js";
import { ServerInfoDocument, type ServerInfoQuery } from "./generated/graphql.js";
import { minServerVersion } from "./generated/manifest.js";
import {
  checkMinimum,
  createProbeCache,
  gateOnProbe,
  reprobeAndCheck,
  type ServerStatus,
  serverStatus,
} from "./serverInfo.js";

export type { ServerStatus } from "./serverInfo.js";

// GraphQL error codes of a gateway rejecting an operation against its schema.
const SCHEMA_MISMATCH_CODES = /* @__PURE__ */ new Set([
  "GRAPHQL_VALIDATION_FAILED",
  "GRAPHQL_PARSE_FAILED",
]);

// A probe that has not answered by then counts as unreachable, so a caller
// held on it proceeds and the next call probes again.
const PROBE_TIMEOUT_MS = 3000;

// Kept apart from the FrameWorksClient cache: these probes are anonymous,
// while a client's carry its token.
const gatewayProbes = /* @__PURE__ */ createProbeCache();

function normalizeGatewayUrl(gatewayUrl: string): string {
  return gatewayUrl.trim().replace(/\/$/, "");
}

async function fetchServerStatus(gatewayUrl: string): Promise<ServerStatus> {
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => {
      controller.abort();
      reject(new Error("Gateway serverInfo probe timed out"));
    }, PROBE_TIMEOUT_MS);
  });
  try {
    return await Promise.race([readServerStatus(gatewayUrl, controller.signal), timeout]);
  } finally {
    clearTimeout(timer);
  }
}

async function readServerStatus(gatewayUrl: string, signal: AbortSignal): Promise<ServerStatus> {
  const response = await fetch(gatewayUrl, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ query: String(ServerInfoDocument), operationName: "ServerInfo" }),
    signal,
  });
  // A v0.3.10 gateway answers an anonymous serverInfo with its x402
  // challenge. It is no verdict; the cache keeps it like an answer.
  if (response.status === 402) {
    throw new PaymentRequiredError("Gateway answered serverInfo with HTTP 402", {
      status: 402,
    });
  }
  let payload: {
    data?: Partial<ServerInfoQuery> | null;
    errors?: Array<{ extensions?: { code?: unknown } }>;
  } | null = null;
  try {
    payload = await response.json();
  } catch {
    payload = null;
  }
  const info = payload?.data?.serverInfo;
  if (info && typeof info.version === "string" && Array.isArray(info.features)) {
    return serverStatus(
      info.version,
      info.features.filter((slug): slug is string => typeof slug === "string")
    );
  }
  // A gateway older than serverInfo rejects the query as a validation error,
  // with HTTP 422 or 200; that is an answer, not a failure.
  if (isSchemaMismatchCode(payload?.errors?.[0]?.extensions?.code)) {
    return { version: null, features: [], verified: false };
  }
  throw new Error(`Gateway serverInfo probe failed with HTTP ${response.status}`);
}

function run(key: string): () => Promise<ServerStatus> {
  return () => fetchServerStatus(key);
}

/**
 * Returns the gateway's serverInfo; version is null when the gateway predates
 * the field. An answer is reused for five minutes. Rejects when the gateway
 * could not be reached or answered something else; only a 402 of those is
 * remembered.
 */
export function probeServerInfo(gatewayUrl: string): Promise<ServerStatus> {
  const key = normalizeGatewayUrl(gatewayUrl);
  return gatewayProbes.probe(key, run(key)).pending;
}

/**
 * Throws ServerTooOldError for a gateway older than the SDK line's minimum
 * server: a stable release below it, or a gateway without serverInfo.
 * Development builds and release candidates are unverified and pass.
 */
export function requireSupportedGateway(status: ServerStatus): void {
  checkMinimum(status, minServerVersion);
}

/**
 * The gateway's serverInfo after the version check, or null when the probe
 * could not reach the gateway: that is not a verdict, so the caller proceeds
 * as on a gateway without optional features and the next call probes again.
 * A cached answer that would refuse the gateway is asked again first, so an
 * upgraded gateway is seen at once. Rejects with ServerTooOldError for an
 * unsupported gateway.
 */
export function checkGateway(gatewayUrl: string): Promise<ServerStatus | null> {
  const key = normalizeGatewayUrl(gatewayUrl);
  return gateOnProbe(
    gatewayProbes,
    key,
    run(key),
    requireSupportedGateway,
    (err) => err instanceof ServerTooOldError
  );
}

/**
 * Asks the gateway again at once, after it rejected a request as invalid
 * against its schema: the gateway may have changed since the cached probe.
 * Rejects with ServerTooOldError when the fresh answer refuses the gateway.
 */
export async function recheckGateway(gatewayUrl: string): Promise<void> {
  const key = normalizeGatewayUrl(gatewayUrl);
  gatewayProbes.forget(key);
  await reprobeAndCheck(gatewayProbes, key, run(key), requireSupportedGateway);
}

/** True for the GraphQL error codes of a gateway rejecting an operation against its schema. */
export function isSchemaMismatchCode(code: unknown): boolean {
  return typeof code === "string" && SCHEMA_MISMATCH_CODES.has(code);
}

/** Forgets every gateway probe answer, so the next call asks each gateway again. */
export function clearServerInfoProbes(): void {
  gatewayProbes.clear();
}
