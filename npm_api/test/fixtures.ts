import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const conformanceDir = fileURLToPath(new URL("../../sdk_conformance/", import.meta.url));

/** Loads one of the language-neutral fixtures every SDK's tests share. */
export function loadFixture<T>(name: string): T {
  return JSON.parse(readFileSync(conformanceDir + name, "utf8")) as T;
}

export interface FixtureResponse {
  status?: number;
  headers?: Record<string, string>;
  body?: unknown;
  bodyText?: string;
  networkError?: boolean | "refused" | "dns" | "reset" | "timeout";
}

// How Node's fetch (undici) reports each fixture network failure: a
// TypeError("fetch failed") whose cause carries the system or undici code.
const networkFailures: Record<string, { message: string; code: string }> = {
  refused: { message: "connect ECONNREFUSED 127.0.0.1:443", code: "ECONNREFUSED" },
  dns: { message: "getaddrinfo ENOTFOUND retry.test", code: "ENOTFOUND" },
  reset: { message: "other side closed", code: "UND_ERR_SOCKET" },
  timeout: { message: "Headers Timeout Error", code: "UND_ERR_HEADERS_TIMEOUT" },
};

/** The error fetch throws for a fixture networkError. */
export function fetchFailure(kind: FixtureResponse["networkError"]): TypeError {
  const failure = networkFailures[kind === true || !kind ? "reset" : kind];
  if (!failure) {
    throw new Error(`unknown fixture networkError ${String(kind)}`);
  }
  return new TypeError("fetch failed", {
    cause: Object.assign(new Error(failure.message), { code: failure.code }),
  });
}

export interface ExpectedError {
  kind: string;
  [field: string]: unknown;
}

/** Answers one scripted response, or throws the way fetch does on a network failure. */
export function respond(r: FixtureResponse): Response {
  if (r.networkError) {
    throw fetchFailure(r.networkError);
  }
  const text = r.bodyText ?? (r.body === undefined ? "" : JSON.stringify(r.body));
  return new Response(text, { status: r.status ?? 200, headers: r.headers });
}

export interface RecordedRequest {
  operationName: string | null;
  headers: Record<string, string>;
  body: { query: string; variables: Record<string, unknown>; operationName?: string };
}

/** A fetch that answers from queues per operation name and records every request. */
export function scriptedFetch(queues: Record<string, FixtureResponse[]>) {
  const requests: RecordedRequest[] = [];
  const fetchFn = async (_url: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const body = JSON.parse(String(init?.body)) as RecordedRequest["body"];
    const name = body.operationName ?? null;
    requests.push({
      operationName: name,
      headers: { ...(init?.headers as Record<string, string>) },
      body,
    });
    const queue = queues[name ?? ""] ?? queues["*"];
    const next = queue?.shift();
    if (!next) {
      throw new Error(`no scripted response left for ${name}`);
    }
    return respond(next);
  };
  return { fetch: fetchFn as typeof fetch, requests };
}

const errorFieldNames: Record<string, string> = {
  status: "status",
  code: "code",
  message: "message",
  retryAfterSeconds: "retryAfterSeconds",
  typename: "typename",
  field: "field",
  resourceType: "resourceType",
  resourceId: "resourceId",
  path: "path",
  serverVersion: "serverVersion",
  minimumVersion: "minimumVersion",
  operation: "operation",
  since: "since",
};

/** Checks an error against a fixture's expected kind and fields. */
export function errorMismatches(err: unknown, expected: ExpectedError): string[] {
  const problems: string[] = [];
  const e = err as Record<string, unknown> & { name?: string };
  if (e?.name !== expected.kind) {
    problems.push(
      `kind ${String(e?.name)} (${String((err as Error)?.message)}), want ${expected.kind}`
    );
  }
  for (const [key, want] of Object.entries(expected)) {
    if (key === "kind") {
      continue;
    }
    const prop = errorFieldNames[key];
    if (!prop) {
      problems.push(`fixture field ${key} has no mapping`);
      continue;
    }
    const got = e?.[prop] ?? null;
    if (JSON.stringify(got) !== JSON.stringify(want)) {
      problems.push(`${key} ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    }
  }
  return problems;
}
