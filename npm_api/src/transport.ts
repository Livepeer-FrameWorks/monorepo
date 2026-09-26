import {
  AuthenticationError,
  type FrameWorksError,
  GraphQLError,
  type GraphQLErrorEntry,
  HTTPError,
  NetworkError,
  PaymentRequiredError,
  ProtocolError,
  RateLimitError,
  SchemaMismatchError,
  ServerError,
} from "./errors.js";
import {
  backoffDelay,
  parseRetryAfter,
  retryableStatuses,
  type RetryPolicy,
  type Sleep,
} from "./retry.js";

export type OperationKind = "query" | "mutation" | "subscription";

/** A bearer token, or a function returning the current one. null or undefined sends no Authorization header. */
export type TokenSource =
  | string
  | null
  | undefined
  | (() => string | null | undefined | Promise<string | null | undefined>);

export async function resolveToken(source: TokenSource): Promise<string | null> {
  const value = typeof source === "function" ? await source() : source;
  return value ? value : null;
}

export interface TransportConfig {
  url: string;
  token: TokenSource;
  fetch: () => typeof fetch;
  headers: Readonly<Record<string, string>>;
  retry: RetryPolicy;
  sleep: Sleep;
}

export interface SendOptions {
  kind: OperationKind;
  operationName: string | null;
  query: string;
  variables: unknown;
  headers?: Readonly<Record<string, string>>;
  idempotencyKey?: string;
  signal?: AbortSignal;
  /** Receives the field errors of a response whose data survived them (see PartialErrors). */
  onPartialErrors?: (errors: ReadonlyArray<GraphQLErrorEntry>) => void;
}

interface GraphQLResponseBody {
  data?: unknown;
  errors?: GraphQLErrorEntry[];
}

/**
 * A failed attempt. retryable says a query may be sent again; unsent says
 * the request provably never reached the server, so a mutation may be sent
 * again too.
 */
type Outcome =
  | { ok: true; data: unknown; partialErrors?: ReadonlyArray<GraphQLErrorEntry> }
  | { ok: false; error: FrameWorksError; retryable: boolean; unsent: boolean };

// Node's fetch reports these codes on the error's cause when the connection
// was refused or the host name did not resolve, both before any byte of the
// request was sent. Browsers do not say why a fetch failed, so there no
// network error counts as unsent.
const unsentCodes = new Set(["ECONNREFUSED", "ENOTFOUND", "EAI_AGAIN"]);

function errorCode(err: unknown): unknown {
  return err !== null && typeof err === "object" ? (err as { code?: unknown }).code : undefined;
}

/** Whether a fetch failure happened before the request was sent. */
export function failedBeforeSend(err: unknown): boolean {
  const cause =
    err !== null && typeof err === "object" ? (err as { cause?: unknown }).cause : undefined;
  if (unsentCodes.has(errorCode(cause) as string)) {
    return true;
  }
  // Node tries each address of a host in turn and reports an AggregateError
  // when every connection attempt failed.
  if (cause instanceof AggregateError && cause.errors.length > 0) {
    return cause.errors.every((e) => unsentCodes.has(errorCode(e) as string));
  }
  return false;
}

/**
 * Posts one GraphQL operation and returns its data, retrying per the policy.
 * Queries retry network errors and 408, 429, and 5xx responses. A mutation,
 * with or without an idempotency key, retries only when the request provably
 * never reached the server: a refused connection, a failed DNS lookup, or a
 * 429. The gateway does not deduplicate replayed mutations, so a mutation is
 * never sent again after a timeout, a connection reset, or a 5xx.
 */
export async function send(config: TransportConfig, options: SendOptions): Promise<unknown> {
  const policy = config.retry;
  for (let attempt = 1; ; attempt++) {
    const outcome = await attemptOnce(config, options);
    if (outcome.ok) {
      if (outcome.partialErrors && outcome.partialErrors.length > 0) {
        options.onPartialErrors?.(outcome.partialErrors);
      }
      return outcome.data;
    }
    const mayRetry =
      options.kind === "query" ? outcome.retryable : outcome.unsent || outcome.error.status === 429;
    if (!mayRetry || attempt >= policy.maxAttempts) {
      throw outcome.error;
    }
    const retryAfter = outcome.error.retryAfterSeconds;
    let delay = backoffDelay(policy, attempt);
    if (retryAfter !== null) {
      if (retryAfter * 1000 > policy.maxRetryAfterMs) {
        throw outcome.error;
      }
      delay = retryAfter * 1000;
    }
    await config.sleep(delay, options.signal);
  }
}

async function attemptOnce(config: TransportConfig, options: SendOptions): Promise<Outcome> {
  const headers: Record<string, string> = {
    "content-type": "application/json",
    accept: "application/graphql-response+json, application/json",
    ...config.headers,
    ...options.headers,
  };
  const token = await resolveToken(config.token);
  if (token) {
    headers.authorization = `Bearer ${token}`;
  }
  if (options.idempotencyKey) {
    headers["idempotency-key"] = options.idempotencyKey;
  }
  const body = JSON.stringify({
    query: options.query,
    variables: options.variables ?? {},
    ...(options.operationName ? { operationName: options.operationName } : {}),
  });

  let response: Response;
  try {
    response = await config.fetch()(config.url, {
      method: "POST",
      headers,
      body,
      signal: options.signal,
    });
  } catch (err) {
    if (options.signal?.aborted) {
      throw err;
    }
    return {
      ok: false,
      retryable: true,
      unsent: failedBeforeSend(err),
      error: new NetworkError(`request to ${config.url} failed: ${describe(err)}`, { cause: err }),
    };
  }

  let text: string;
  try {
    text = await response.text();
  } catch (err) {
    if (options.signal?.aborted) {
      throw err;
    }
    return {
      ok: false,
      retryable: true,
      unsent: false,
      error: new NetworkError(`reading the response from ${config.url} failed: ${describe(err)}`, {
        cause: err,
      }),
    };
  }
  return classify(response.status, response.headers.get("retry-after"), text);
}

function describe(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * extensions.code values that fail a call wherever they appear, even below a
 * root field that came back.
 */
const fatalCodes = new Set([
  "UNAUTHORIZED",
  "RATE_LIMITED",
  "GRAPHQL_VALIDATION_FAILED",
  "GRAPHQL_PARSE_FAILED",
]);

/**
 * Whether every GraphQL error is a field error that left the response's data
 * usable: it has a path below a root field whose value is not null, and it is
 * not an authentication, rate-limit, or document error. GraphQL sets a failed
 * field to null and carries the null up to the nearest nullable ancestor, so
 * data that passes still matches the operation's types. An error without a
 * path, or one that nulled a root field, fails the call.
 */
function fieldErrorsOnly(data: unknown, errors: ReadonlyArray<GraphQLErrorEntry>): boolean {
  if (data === null || typeof data !== "object" || Array.isArray(data)) {
    return false;
  }
  const roots = data as Record<string, unknown>;
  return errors.every((entry) => {
    const code = entry?.extensions?.code;
    if (typeof code === "string" && fatalCodes.has(code)) {
      return false;
    }
    const path = entry?.path;
    if (!Array.isArray(path) || path.length < 2 || typeof path[0] !== "string") {
      return false;
    }
    const root = roots[path[0]];
    return root !== undefined && root !== null;
  });
}

/**
 * Turns one HTTP response into data or a typed error. A 2xx response whose
 * errors are all field errors that left its data usable (fieldErrorsOnly)
 * returns the data with those errors as partialErrors.
 */
export function classify(status: number, retryAfterHeader: string | null, text: string): Outcome {
  const retryAfterSeconds = parseRetryAfter(retryAfterHeader);
  const retryable = retryableStatuses.has(status);
  const is2xx = status >= 200 && status < 300;
  let body: unknown;
  try {
    body = text === "" ? undefined : JSON.parse(text);
  } catch {
    body = undefined;
  }
  const obj =
    body !== null && typeof body === "object" && !Array.isArray(body)
      ? (body as Record<string, unknown>)
      : null;

  if (obj && Array.isArray(obj.errors) && obj.errors.length > 0) {
    const gql = obj as GraphQLResponseBody;
    if (is2xx && fieldErrorsOnly(gql.data, gql.errors ?? [])) {
      return { ok: true, data: gql.data, partialErrors: gql.errors ?? [] };
    }
    return {
      ok: false,
      retryable,
      unsent: false,
      error: graphQLError(
        gql.errors ?? [],
        gql.data ?? null,
        is2xx ? null : status,
        retryAfterSeconds,
        body
      ),
    };
  }
  if (!is2xx) {
    return {
      ok: false,
      retryable,
      unsent: false,
      error: httpError(status, obj, text, retryAfterSeconds, body),
    };
  }
  if (!obj) {
    return {
      ok: false,
      retryable: false,
      unsent: false,
      error: new ProtocolError(`response is not JSON (HTTP ${status})`, { status, body: text }),
    };
  }
  if (obj.data === undefined || obj.data === null) {
    return {
      ok: false,
      retryable: false,
      unsent: false,
      error: new ProtocolError(`response has no data (HTTP ${status})`, { status, body }),
    };
  }
  return { ok: true, data: obj.data };
}

function httpError(
  status: number,
  obj: Record<string, unknown> | null,
  text: string,
  retryAfterSeconds: number | null,
  body: unknown
): FrameWorksError {
  const code = typeof obj?.code === "string" ? obj.code : null;
  const message =
    (typeof obj?.message === "string" && obj.message) ||
    (typeof obj?.error === "string" && obj.error) ||
    text.trim().slice(0, 200) ||
    `HTTP ${status}`;
  const details = { status, code, retryAfterSeconds, body: body ?? text };
  if (status === 401) {
    return new AuthenticationError(message, details);
  }
  if (status === 402) {
    return new PaymentRequiredError(message, details);
  }
  if (status === 429) {
    return new RateLimitError(message, details);
  }
  if (status >= 500) {
    return new ServerError(message, details);
  }
  return new HTTPError(message, details);
}

/** Maps GraphQL errors by the extensions.code of the first entry. */
export function graphQLError(
  errors: ReadonlyArray<GraphQLErrorEntry>,
  data: unknown,
  status: number | null,
  retryAfterSeconds: number | null,
  body: unknown
): FrameWorksError {
  const first = errors[0];
  const code = typeof first?.extensions?.code === "string" ? first.extensions.code : null;
  const message = first?.message ?? "GraphQL error";
  const details = { status, code, retryAfterSeconds, body };
  switch (code) {
    case "UNAUTHORIZED":
      return new AuthenticationError(message, details);
    case "RATE_LIMITED":
      return new RateLimitError(message, details);
    case "GRAPHQL_VALIDATION_FAILED":
    case "GRAPHQL_PARSE_FAILED":
      return new SchemaMismatchError(message, errors, data, details);
    default:
      return new GraphQLError(message, errors, data, details);
  }
}
