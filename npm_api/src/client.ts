import type { DocumentTypeDecoration } from "@graphql-typed-document-node/core";
import { getOperationAST, parse } from "graphql";

import { ServerInfoDocument } from "./generated/graphql.js";
import { minServerVersion, operations } from "./generated/manifest.js";
import { defaultRetryPolicy, defaultSleep, type RetryPolicy, type Sleep } from "./retry.js";
import { SchemaMismatchError, ServerTooOldError, UnsupportedOperationError } from "./errors.js";
import {
  checkMinimum,
  checkOperation,
  compareVersions,
  createProbeCache,
  gateOnProbe,
  parseStableVersion,
  predatesServerInfo,
  reprobeAndCheck,
  type ServerStatus,
  serverStatus,
} from "./serverInfo.js";
import { type OperationKind, send, type TokenSource, type TransportConfig } from "./transport.js";

export interface ClientOptions {
  /** The GraphQL endpoint, e.g. https://bridge.example.com/graphql. */
  url: string;
  /** Bearer token, or a function returning the current one; it is read for every attempt. */
  token?: TokenSource;
  /** fetch implementation; defaults to the global fetch. */
  fetch?: typeof fetch;
  /** Headers sent with every request. */
  headers?: Readonly<Record<string, string>>;
  /** Overrides of the default retry policy. */
  retry?: Partial<RetryPolicy>;
  /**
   * Check the server's version before operations (default true). The
   * serverInfo answer is shared per URL and asked again after five minutes,
   * before a cached answer fails a call as too old, and when an operation
   * fails schema validation. With the check off, the client never throws
   * ServerTooOldError or UnsupportedOperationError.
   */
  checkServer?: boolean;
}

export interface RequestOptions {
  /** Selects an operation when the document contains more than one. */
  operationName?: string;
  signal?: AbortSignal;
  /** Headers for this request only. */
  headers?: Readonly<Record<string, string>>;
  /**
   * Sent as Idempotency-Key. Paid mutations settled with x402 require one,
   * and the gateway deduplicates those by it; reuse the same key when you
   * retry the call yourself. The client retries a mutation only when the
   * request provably never reached the server (a refused connection, a
   * failed DNS lookup, or a 429), never after a timeout, a reset, or a 5xx.
   */
  idempotencyKey?: string;
  /** A viewer's playback JWT, sent as X-Frameworks-Playback-JWT (resolveViewerEndpoint). */
  playbackToken?: string;
}

type VariablesArgs<TVariables> =
  Record<string, never> extends TVariables
    ? [variables?: TVariables, options?: RequestOptions]
    : [variables: TVariables, options: RequestOptions] | [variables: TVariables];

export interface FrameWorksClient {
  readonly url: string;
  /** Runs one query or mutation and returns its data. Subscriptions use the ./subscriptions entry. */
  request<TResult, TVariables>(
    document: DocumentTypeDecoration<TResult, TVariables> | string,
    ...args: VariablesArgs<TVariables>
  ): Promise<TResult>;
  /** The server's version and features, from the cached serverInfo probe. */
  serverInfo(options?: { signal?: AbortSignal }): Promise<ServerStatus>;
}

/** Test and tooling hooks that are not part of the public API. */
export interface InternalOptions {
  sleep?: Sleep;
  operationSince?: Readonly<Record<string, string>>;
}

interface ParsedOperation {
  kind: OperationKind;
  name: string | null;
}

export function parseOperation(document: string, operationName?: string): ParsedOperation {
  const operation = getOperationAST(parse(document), operationName);
  if (!operation) {
    throw new TypeError("document must select exactly one GraphQL operation");
  }
  return { kind: operation.operation, name: operation.name?.value ?? null };
}

/** serverInfo answers by GraphQL URL, shared by every client in the process. */
const clientProbes = /* @__PURE__ */ createProbeCache();

function isVersionVerdict(err: unknown): boolean {
  return err instanceof ServerTooOldError || err instanceof UnsupportedOperationError;
}

// A caller can leave a shared probe without cancelling it for other callers.
function withSignal<T>(pending: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) {
    return pending;
  }
  return new Promise<T>((resolve, reject) => {
    const abort = () => reject(signal.reason);
    if (signal.aborted) {
      abort();
    } else {
      signal.addEventListener("abort", abort, { once: true });
    }
    pending.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
  });
}

function newerThanMinimum(since: string | undefined): boolean {
  if (!since) {
    return false;
  }
  const a = parseStableVersion(since);
  const b = parseStableVersion(minServerVersion);
  return a !== null && b !== null && compareVersions(a, b) > 0;
}

export function createClient(options: ClientOptions): FrameWorksClient {
  return createClientWith(options, {});
}

export function createClientWith(
  options: ClientOptions,
  internal: InternalOptions
): FrameWorksClient {
  if (!options.url) {
    throw new TypeError("createClient: url is required");
  }
  const config: TransportConfig = {
    url: options.url,
    token: options.token,
    fetch: () => options.fetch ?? globalThis.fetch.bind(globalThis),
    headers: options.headers ?? {},
    retry: { ...defaultRetryPolicy, ...options.retry },
    sleep: internal.sleep ?? defaultSleep,
  };
  const checkServer = options.checkServer ?? true;
  const since = (name: string | null): string | undefined => {
    if (!name) {
      return undefined;
    }
    if (internal.operationSince && name in internal.operationSince) {
      return internal.operationSince[name];
    }
    return (operations as Record<string, { since: string }>)[name]?.since;
  };

  const runProbe = async (): Promise<ServerStatus> => {
    try {
      const data = (await send(config, {
        kind: "query",
        operationName: "ServerInfo",
        query: String(ServerInfoDocument),
        variables: {},
        signal: AbortSignal.timeout(30_000),
      })) as { serverInfo: { version: string; features: string[] } };
      return serverStatus(data.serverInfo.version, data.serverInfo.features);
    } catch (err) {
      if (predatesServerInfo(err)) {
        return { version: null, features: [], verified: false };
      }
      throw err;
    }
  };

  const check =
    (name: string | null) =>
    (status: ServerStatus): void => {
      checkMinimum(status, minServerVersion);
      if (name) {
        checkOperation(status, name, since(name));
      }
    };

  // A failed probe (or a 402) is not a verdict: the operation proceeds and
  // its own response decides the outcome.
  const gate = async (name: string | null): Promise<void> => {
    await gateOnProbe(clientProbes, options.url, runProbe, check(name), isVersionVerdict);
  };

  // An operation the server rejects as invalid against its schema may mean
  // the server changed since the cached probe, so it is asked again at once.
  const recheckOnSchemaMismatch = async <T>(
    result: Promise<T>,
    name: string | null,
    signal?: AbortSignal
  ): Promise<T> => {
    try {
      return await result;
    } catch (err) {
      if (!(err instanceof SchemaMismatchError)) {
        throw err;
      }
      clientProbes.forget(options.url);
      await withSignal(reprobeAndCheck(clientProbes, options.url, runProbe, check(name)), signal);
      throw err;
    }
  };

  return {
    url: options.url,
    async request<TResult, TVariables>(
      document: DocumentTypeDecoration<TResult, TVariables> | string,
      ...args: VariablesArgs<TVariables>
    ): Promise<TResult> {
      const [variables, requestOptions = {}] = args as [
        TVariables | undefined,
        RequestOptions | undefined,
      ];
      const query = String(document);
      const op = parseOperation(query, requestOptions.operationName);
      if (op.kind === "subscription") {
        throw new TypeError(
          "subscriptions run over WebSocket; use @livepeer-frameworks/api/subscriptions"
        );
      }
      const headers: Record<string, string> = { ...requestOptions.headers };
      if (requestOptions.playbackToken) {
        headers["x-frameworks-playback-jwt"] = requestOptions.playbackToken;
      }
      const run = () =>
        send(config, {
          kind: op.kind,
          operationName: op.name,
          query,
          variables,
          headers,
          idempotencyKey: requestOptions.idempotencyKey,
          signal: requestOptions.signal,
        }) as Promise<TResult>;

      if (!checkServer || op.name === "ServerInfo") {
        return run();
      }
      // Mutations, and queries newer than the line's minimum server, wait for
      // the probe so an unsupported call is never sent.
      if (op.kind === "mutation" || newerThanMinimum(since(op.name))) {
        await withSignal(gate(op.name), requestOptions.signal);
        return recheckOnSchemaMismatch(run(), op.name, requestOptions.signal);
      }
      // Other queries run alongside the probe; the result is held until the
      // probe settles so a too-old server still fails the call.
      const result = run();
      result.catch(() => undefined);
      await withSignal(gate(op.name), requestOptions.signal);
      return recheckOnSchemaMismatch(result, op.name, requestOptions.signal);
    },
    serverInfo: (requestOptions) =>
      withSignal(clientProbes.probe(options.url, runProbe).pending, requestOptions?.signal),
  };
}
