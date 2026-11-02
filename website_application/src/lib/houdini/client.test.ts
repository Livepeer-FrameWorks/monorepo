import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("$app/environment", () => ({ browser: true }));
vi.mock("$houdini", () => ({ HoudiniClient: class {} }));
vi.mock("$houdini/plugins", () => ({ fetch: vi.fn(), subscription: vi.fn() }));
vi.mock("graphql-ws", () => ({ createClient: vi.fn() }));
vi.mock("$lib/houdini/realtime-ws", () => ({ realtimeClientOptions: vi.fn() }));
const refreshAuthSession = vi.fn();
vi.mock("$lib/auth/refresh", () => ({ refreshAuthSession }));
const endSession = vi.fn(async () => {});
vi.mock("$lib/houdini/session-end", () => ({ endSession }));

const { graphQLFetch } = await import("./client");

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function request(fetch: typeof globalThis.fetch) {
  return graphQLFetch({
    fetch,
    name: "GetBillingStatus",
    text: "query GetBillingStatus { billingStatus { billingStatus } }",
    variables: {},
    session: null,
  } as unknown as Parameters<typeof graphQLFetch>[0]);
}

afterEach(() => {
  refreshAuthSession.mockReset();
  endSession.mockClear();
});

describe("graphQLFetch session handling", () => {
  it("signs out and does not repost when the refresh after a 401 is refused", async () => {
    refreshAuthSession.mockResolvedValue("unauthorized");
    const fetch = vi.fn(async () =>
      jsonResponse(401, { errors: [{ message: "unauthenticated" }] })
    );
    const result = await request(fetch as unknown as typeof globalThis.fetch);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(endSession).toHaveBeenCalledWith("expired");
    expect(result.errors[0].message).toBe("unauthenticated");
  });

  it("reposts once after a successful refresh and keeps the session", async () => {
    refreshAuthSession.mockResolvedValue("ok");
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { errors: [{ message: "unauthenticated" }] }))
      .mockResolvedValueOnce(
        jsonResponse(200, { data: { billingStatus: { billingStatus: "active" } } })
      );
    const result = await request(fetch as unknown as typeof globalThis.fetch);
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(endSession).not.toHaveBeenCalled();
    expect(result.data.billingStatus.billingStatus).toBe("active");
  });

  it("keeps the session when the refresh could not reach the gateway", async () => {
    refreshAuthSession.mockResolvedValue("transient");
    const fetch = vi.fn(async () =>
      jsonResponse(401, { errors: [{ message: "unauthenticated" }] })
    );
    await request(fetch as unknown as typeof globalThis.fetch);
    expect(endSession).not.toHaveBeenCalled();
  });
});
