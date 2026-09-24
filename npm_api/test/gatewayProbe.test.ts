import { afterEach, describe, expect, it, vi } from "vitest";

import { ServerTooOldError } from "../src/errors.js";
import {
  checkGateway,
  checkGatewayFor,
  clearServerInfoProbes,
  recheckGatewayFor,
} from "../src/gatewayProbe.js";
import type * as manifest from "../src/generated/manifest.js";
import type { OperationName } from "../src/generated/manifest.js";

// A line whose minimum server is newer than the operations a player sends:
// the operation gate must accept a gateway the line gate refuses.
vi.mock("../src/generated/manifest.js", async (importOriginal) => {
  const actual = await importOriginal<typeof manifest>();
  return {
    ...actual,
    minServerVersion: "v0.4.0",
    operations: {
      ...actual.operations,
      ServerInfo: { ...actual.operations.ServerInfo, since: "v0.3.11" },
      ResolveViewerEndpoint: { ...actual.operations.ResolveViewerEndpoint, since: "v0.3.11" },
      ResolveIngestEndpoint: { ...actual.operations.ResolveIngestEndpoint, since: "v0.3.13" },
    },
  };
});

const URL_UNDER_TEST = "https://gate.example/graphql";
const VIEWER: readonly OperationName[] = ["ServerInfo", "ResolveViewerEndpoint"];
const INGEST: readonly OperationName[] = ["ServerInfo", "ResolveIngestEndpoint"];

function serverInfo(version: string): Response {
  return new Response(JSON.stringify({ data: { serverInfo: { version, features: [] } } }));
}

function stubFetch(...responses: Array<() => Response>) {
  const queue = [...responses];
  const fetcher = vi.fn(async () => {
    const next = queue.shift();
    if (!next) throw new Error("no scripted serverInfo response left");
    return next();
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}

async function outcome<T>(pending: Promise<T>): Promise<{ value: T } | { error: unknown }> {
  return pending.then(
    (value) => ({ value }),
    (error: unknown) => ({ error })
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  clearServerInfoProbes();
});

describe("checkGatewayFor", () => {
  it("passes a gateway serving the operations though it is below the line minimum", async () => {
    // The line gate asks again before refusing a cached answer.
    stubFetch(
      () => serverInfo("v0.3.12"),
      () => serverInfo("v0.3.12")
    );
    const status = await checkGatewayFor(URL_UNDER_TEST, VIEWER);
    expect(status?.version).toBe("v0.3.12");
    const line = await outcome(checkGateway(URL_UNDER_TEST));
    expect("error" in line && line.error).toBeInstanceOf(ServerTooOldError);
    expect((line as { error: ServerTooOldError }).error.minimumVersion).toBe("v0.4.0");
  });

  it("refuses a gateway below the newest since of the operations", async () => {
    const fetcher = stubFetch(
      () => serverInfo("v0.3.12"),
      () => serverInfo("v0.3.12")
    );
    const result = await outcome(checkGatewayFor(URL_UNDER_TEST, INGEST));
    expect("error" in result && result.error).toBeInstanceOf(ServerTooOldError);
    const error = (result as { error: ServerTooOldError }).error;
    expect(error.serverVersion).toBe("v0.3.12");
    expect(error.minimumVersion).toBe("v0.3.13");
    // A cached refusal is asked again before it fails the next call.
    await expect(checkGatewayFor(URL_UNDER_TEST, INGEST)).rejects.toBeInstanceOf(ServerTooOldError);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("refuses a gateway below every operation's since", async () => {
    stubFetch(() => serverInfo("v0.3.10"));
    await expect(checkGatewayFor(URL_UNDER_TEST, VIEWER)).rejects.toMatchObject({
      name: "ServerTooOldError",
      serverVersion: "v0.3.10",
      minimumVersion: "v0.3.11",
    });
  });

  it("refuses a gateway without serverInfo, naming the operations' release", async () => {
    stubFetch(
      () =>
        new Response(
          JSON.stringify({ errors: [{ extensions: { code: "GRAPHQL_VALIDATION_FAILED" } }] }),
          { status: 422 }
        )
    );
    await expect(checkGatewayFor(URL_UNDER_TEST, VIEWER)).rejects.toMatchObject({
      name: "ServerTooOldError",
      serverVersion: null,
      minimumVersion: "v0.3.11",
    });
  });

  it("passes development builds and release candidates unverified", async () => {
    for (const version of ["dev", "v0.3.12-4-gabcdef", "v0.3.13-rc1"]) {
      clearServerInfoProbes();
      stubFetch(() => serverInfo(version));
      const status = await checkGatewayFor(URL_UNDER_TEST, INGEST);
      expect(status?.verified).toBe(false);
    }
  });

  it("treats a 402 as no verdict and keeps it cached", async () => {
    const fetcher = stubFetch(() => new Response("", { status: 402 }));
    expect(await checkGatewayFor(URL_UNDER_TEST, INGEST)).toBeNull();
    expect(await checkGatewayFor(URL_UNDER_TEST, INGEST)).toBeNull();
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("throws TypeError for an unknown operation or an empty list before probing", () => {
    const fetcher = stubFetch();
    expect(() => checkGatewayFor(URL_UNDER_TEST, ["NotAnOperation" as OperationName])).toThrow(
      TypeError
    );
    expect(() => checkGatewayFor(URL_UNDER_TEST, ["toString" as OperationName])).toThrow(TypeError);
    expect(() => checkGatewayFor(URL_UNDER_TEST, [])).toThrow(TypeError);
    expect(() => recheckGatewayFor(URL_UNDER_TEST, ["NotAnOperation" as OperationName])).toThrow(
      TypeError
    );
    expect(fetcher).not.toHaveBeenCalled();
  });
});

describe("recheckGatewayFor", () => {
  it("asks again at once and applies the operations' requirement", async () => {
    const fetcher = stubFetch(
      () => serverInfo("v0.3.13"),
      () => serverInfo("v0.3.12"),
      () => serverInfo("v0.3.12")
    );
    await checkGatewayFor(URL_UNDER_TEST, INGEST);
    await expect(recheckGatewayFor(URL_UNDER_TEST, INGEST)).rejects.toMatchObject({
      name: "ServerTooOldError",
      serverVersion: "v0.3.12",
      minimumVersion: "v0.3.13",
    });
    await expect(recheckGatewayFor(URL_UNDER_TEST, VIEWER)).resolves.toBeUndefined();
    expect(fetcher).toHaveBeenCalledTimes(3);
  });
});
