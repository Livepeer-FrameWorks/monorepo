/**
 * Typed errors. Every error the SDK throws for a failed call is a
 * FrameWorksError; the subclass says what failed. The class names are shared
 * with the Go and Python SDKs.
 */

export interface ErrorDetails {
  /** HTTP status of the response, or null when the failure was not an HTTP status. */
  status?: number | null;
  /** Machine-readable code: extensions.code of a GraphQL error or the code field of an HTTP error body. */
  code?: string | null;
  /** Seconds the server asked the client to wait, from Retry-After or the error itself. */
  retryAfterSeconds?: number | null;
  /** The parsed response body, when there was one. */
  body?: unknown;
  cause?: unknown;
}

export class FrameWorksError extends Error {
  readonly status: number | null;
  readonly code: string | null;
  readonly retryAfterSeconds: number | null;
  readonly body: unknown;

  constructor(message: string, details: ErrorDetails = {}) {
    super(message, details.cause === undefined ? undefined : { cause: details.cause });
    this.name = "FrameWorksError";
    this.status = details.status ?? null;
    this.code = details.code ?? null;
    this.retryAfterSeconds = details.retryAfterSeconds ?? null;
    this.body = details.body;
  }
}

/** The request never produced a response: DNS, TCP, TLS, or a reset connection. */
export class NetworkError extends FrameWorksError {
  constructor(message: string, details: ErrorDetails = {}) {
    super(message, details);
    this.name = "NetworkError";
  }
}

/** A non-2xx response without a GraphQL body that no more specific class covers. */
export class HTTPError extends FrameWorksError {
  constructor(message: string, details: ErrorDetails = {}) {
    super(message, details);
    this.name = "HTTPError";
  }
}

/**
 * The credentials were missing, invalid, or expired: HTTP 401, a GraphQL
 * UNAUTHORIZED error, or a subscription socket closed before it was
 * acknowledged.
 */
export class AuthenticationError extends FrameWorksError {
  constructor(message: string, details: ErrorDetails = {}) {
    super(message, details);
    this.name = "AuthenticationError";
  }
}

/** HTTP 402: the call needs a balance, a payment method, or an x402 payment (see body). */
export class PaymentRequiredError extends FrameWorksError {
  constructor(message: string, details: ErrorDetails = {}) {
    super(message, details);
    this.name = "PaymentRequiredError";
  }
}

/** HTTP 429 or a GraphQL RATE_LIMITED error. retryAfterSeconds says when to try again. */
export class RateLimitError extends FrameWorksError {
  constructor(message: string, details: ErrorDetails = {}) {
    super(message, details);
    this.name = "RateLimitError";
  }
}

/** An HTTP 5xx response. */
export class ServerError extends FrameWorksError {
  constructor(message: string, details: ErrorDetails = {}) {
    super(message, details);
    this.name = "ServerError";
  }
}

/** The response was not a GraphQL response: not JSON, or neither data nor errors. */
export class ProtocolError extends FrameWorksError {
  constructor(message: string, details: ErrorDetails = {}) {
    super(message, details);
    this.name = "ProtocolError";
  }
}

export interface GraphQLErrorEntry {
  message: string;
  path?: ReadonlyArray<string | number>;
  locations?: ReadonlyArray<{ line: number; column: number }>;
  extensions?: Record<string, unknown>;
}

/**
 * The GraphQL errors of a call that still returned its data: each failed a
 * field below a root field that came back, which the server set to null (for
 * example Stream.metrics for an API token without analytics:read). The call
 * resolves with its data; these reach onPartialErrors of the request or the
 * client. Errors without a path, errors that null a root field, and
 * UNAUTHORIZED, RATE_LIMITED, and document errors reject the call instead.
 */
export interface PartialErrors {
  /** The operation name, or null for an anonymous document. */
  operationName: string | null;
  errors: ReadonlyArray<GraphQLErrorEntry>;
}

/** The server answered with GraphQL errors. errors holds every entry; data holds any partial result. */
export class GraphQLError extends FrameWorksError {
  readonly errors: ReadonlyArray<GraphQLErrorEntry>;
  readonly path: ReadonlyArray<string | number> | null;
  readonly data: unknown;

  constructor(
    message: string,
    errors: ReadonlyArray<GraphQLErrorEntry>,
    data: unknown,
    details: ErrorDetails = {}
  ) {
    super(message, details);
    this.name = "GraphQLError";
    this.errors = errors;
    this.path = errors[0]?.path ?? null;
    this.data = data;
  }
}

/**
 * The server rejected the document itself (GRAPHQL_VALIDATION_FAILED or
 * GRAPHQL_PARSE_FAILED): it does not know a field or argument this SDK sends.
 */
export class SchemaMismatchError extends GraphQLError {
  constructor(
    message: string,
    errors: ReadonlyArray<GraphQLErrorEntry>,
    data: unknown,
    details: ErrorDetails = {}
  ) {
    super(message, errors, data, details);
    this.name = "SchemaMismatchError";
  }
}

/** An error member of a result union, raised by expectResult. */
export class ResultError extends FrameWorksError {
  readonly typename: string;
  /** The input field a ValidationError names. */
  readonly field: string | null;
  readonly constraint: string | null;
  readonly resourceType: string | null;
  readonly resourceId: string | null;
  /** The union member as the server returned it. */
  readonly result: Readonly<Record<string, unknown>>;

  constructor(result: Readonly<Record<string, unknown>>) {
    const str = (key: string): string | null =>
      typeof result[key] === "string" ? (result[key] as string) : null;
    const typename = str("__typename") ?? "unknown";
    super(str("message") ?? `unexpected result ${typename}`, {
      code: str("code"),
      retryAfterSeconds: typeof result.retryAfter === "number" ? result.retryAfter : null,
    });
    this.name = "ResultError";
    this.typename = typename;
    this.field = str("field");
    this.constraint = str("constraint");
    this.resourceType = str("resourceType");
    this.resourceId = str("resourceId");
    this.result = result;
  }
}

/** The server is a stable release older than the oldest release this SDK line supports. */
export class ServerTooOldError extends FrameWorksError {
  /** The server's version, or null when the server predates serverInfo. */
  readonly serverVersion: string | null;
  readonly minimumVersion: string;

  constructor(serverVersion: string | null, minimumVersion: string) {
    super(
      serverVersion
        ? `FrameWorks server ${serverVersion} is older than ${minimumVersion}, the oldest release this client can use`
        : `FrameWorks server predates serverInfo; this client needs ${minimumVersion} or later`
    );
    this.name = "ServerTooOldError";
    this.serverVersion = serverVersion;
    this.minimumVersion = minimumVersion;
  }
}

/** The operation was added in a release newer than the server. */
export class UnsupportedOperationError extends FrameWorksError {
  readonly operation: string;
  readonly since: string;
  readonly serverVersion: string;

  constructor(operation: string, since: string, serverVersion: string) {
    super(`${operation} needs FrameWorks ${since} or later; the server runs ${serverVersion}`);
    this.name = "UnsupportedOperationError";
    this.operation = operation;
    this.since = since;
    this.serverVersion = serverVersion;
  }
}

/** A VOD upload failed while sending its parts. The upload was aborted. */
export class UploadError extends FrameWorksError {
  readonly uploadId: string;
  readonly partNumber: number | null;

  constructor(
    message: string,
    uploadId: string,
    partNumber: number | null,
    details: ErrorDetails = {}
  ) {
    super(message, details);
    this.name = "UploadError";
    this.uploadId = uploadId;
    this.partNumber = partNumber;
  }
}
