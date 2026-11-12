import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ refresh: vi.fn(), sessions: vi.fn(), activity: vi.fn() }));
vi.mock("$lib/auth/refresh", () => ({ refreshAuthSession: mocks.refresh }));
vi.mock("$lib/authAPI", () => ({ authAPI: { get: vi.fn() } }));
vi.mock("$houdini", async () => {
  const { readable } = await import("svelte/store");
  return {
    fragment: (value: unknown) => readable(value),
    AuthSessionFieldsStore: class {},
    SecurityActivityFieldsStore: class {},
    GetAuthSessionsStore: class {
      fetch = mocks.sessions;
    },
    GetSecurityActivityStore: class {
      fetch = mocks.activity;
    },
    RevokeAuthSessionStore: class {},
    RevokeOtherAuthSessionsStore: class {},
    RenameAuthSessionStore: class {},
    ChangePasswordStore: class {},
    LinkEmailStore: class {},
    LinkWalletStore: class {},
    UnlinkWalletStore: class {},
  };
});
interface MutationResponse {
  errors: null;
  data: { result: Record<string, unknown> };
}
const { securityAction, loadSecuritySessions, loadSecurityActivity } =
  await import("$lib/account-security");
beforeEach(() => {
  vi.clearAllMocks();
  mocks.refresh.mockResolvedValue("ok");
});

describe("personal security API", () => {
  it("renews a legacy credential only after an explicit refusal and retries once", async () => {
    const run = vi
      .fn()
      .mockResolvedValueOnce({
        errors: null,
        data: {
          result: {
            __typename: "AuthError",
            code: "SESSION_REFRESH_REQUIRED",
            message: "Renew first",
          },
        },
      })
      .mockResolvedValueOnce({
        errors: null,
        data: { result: { __typename: "RevokeAuthSessionPayload", success: true, current: false } },
      });
    const result = await securityAction(run, (response: MutationResponse) => response.data.result);
    expect(result.success).toBe(true);
    expect(run).toHaveBeenCalledTimes(2);
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
  });
  it("does not replay a mutation after an outage with an uncertain commit", async () => {
    const run = vi.fn().mockRejectedValue(new Error("service unavailable"));
    await expect(securityAction(run, () => null)).rejects.toThrow("service unavailable");
    expect(run).toHaveBeenCalledTimes(1);
    expect(mocks.refresh).not.toHaveBeenCalled();
  });
  it("does not turn a failed renewal into mutation execution", async () => {
    mocks.refresh.mockResolvedValue("transient");
    const run = vi.fn().mockResolvedValue({
      errors: null,
      data: {
        result: {
          __typename: "AuthError",
          code: "SESSION_REFRESH_REQUIRED",
          message: "Renew first",
        },
      },
    });
    await expect(
      securityAction(run, (response: MutationResponse) => response.data.result)
    ).rejects.toThrow("renewal is unavailable");
    expect(run).toHaveBeenCalledTimes(1);
  });
  it("stops after a second explicit refusal and preserves ordinary validation errors", async () => {
    const run = vi.fn().mockResolvedValue({
      errors: null,
      data: {
        result: {
          __typename: "AuthError",
          code: "SESSION_REFRESH_REQUIRED",
          message: "Still old",
        },
      },
    });
    await expect(
      securityAction(run, (response: MutationResponse) => response.data.result)
    ).rejects.toThrow("Still old");
    expect(run).toHaveBeenCalledTimes(2);
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
    mocks.refresh.mockClear();
    const invalid = vi.fn().mockResolvedValue({
      errors: null,
      data: {
        result: {
          __typename: "ValidationError",
          code: "INVALID_PASSWORD",
          message: "Wrong current password",
        },
      },
    });
    await expect(
      securityAction(invalid, (response: MutationResponse) => response.data.result)
    ).rejects.toThrow("Wrong current password");
    expect(invalid).toHaveBeenCalledTimes(1);
    expect(mocks.refresh).not.toHaveBeenCalled();
  });
  it("fetches bounded fresh pages without accepting user or tenant selectors", async () => {
    const session = { id: "session-a", label: "Laptop", ipAddress: "203.0.113.x" };
    mocks.sessions.mockResolvedValue({
      errors: null,
      data: {
        authSessionsConnection: {
          nodes: [session],
          totalCount: 1,
          pageInfo: { endCursor: "next", hasNextPage: false },
        },
      },
    });
    const page = await loadSecuritySessions("opaque-cursor");
    expect(mocks.sessions).toHaveBeenCalledWith({
      variables: { first: 50, after: "opaque-cursor" },
      policy: "NetworkOnly",
    });
    expect(page.nodes[0]).toEqual(session);
    mocks.activity.mockResolvedValue({
      errors: null,
      data: {
        securityActivityConnection: {
          nodes: [{ id: "event-a" }],
          totalCount: 1,
          pageInfo: { endCursor: null, hasNextPage: false },
        },
      },
    });
    await loadSecurityActivity();
    expect(mocks.activity).toHaveBeenCalledWith({
      variables: { first: 25, after: undefined },
      policy: "NetworkOnly",
    });
  });
});
