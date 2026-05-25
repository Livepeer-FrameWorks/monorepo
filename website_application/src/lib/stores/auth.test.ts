import { beforeEach, describe, expect, it, vi } from "vitest";

function installLocalStorage() {
  const values = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      values.set(key, value);
    }),
    removeItem: vi.fn((key: string) => {
      values.delete(key);
    }),
    clear: vi.fn(() => {
      values.clear();
    }),
  });
}

describe("auth store", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    installLocalStorage();
  });

  it("shares concurrent auth checks so only one refresh request is sent", async () => {
    const authAPI = {
      get: vi.fn().mockRejectedValue(new Error("expired")),
      post: vi.fn().mockResolvedValue({
        data: {
          user: {
            id: "user-1",
            email: "user@example.com",
            tenant_id: "tenant-1",
          },
        },
      }),
    };

    vi.doMock("$lib/authAPI.js", () => ({ authAPI }));
    vi.doMock("$app/environment", () => ({ browser: false }));
    vi.doMock("./realtime.js", () => ({
      initializeWebSocket: vi.fn(),
      disconnectWebSocket: vi.fn(),
    }));

    const { auth } = await import("./auth");

    await Promise.all([auth.checkAuth(), auth.checkAuth()]);

    expect(authAPI.get).toHaveBeenCalledTimes(1);
    expect(authAPI.post).toHaveBeenCalledTimes(1);
    expect(authAPI.post).toHaveBeenCalledWith("/refresh");
  });

  it("preserves unverified-account login error codes for route handling", async () => {
    const authAPI = {
      get: vi.fn(),
      post: vi.fn().mockRejectedValue({
        response: {
          data: {
            error: "email not verified",
            error_code: "EMAIL_NOT_VERIFIED",
          },
        },
      }),
    };

    vi.doMock("$lib/authAPI.js", () => ({ authAPI }));
    vi.doMock("$app/environment", () => ({ browser: false }));
    vi.doMock("./realtime.js", () => ({
      initializeWebSocket: vi.fn(),
      disconnectWebSocket: vi.fn(),
    }));

    const { auth } = await import("./auth");

    const result = await auth.login("user@example.com", "correct-password", {
      human_check: "human",
    });

    expect(result).toEqual({
      success: false,
      error: "email not verified",
      errorCode: "EMAIL_NOT_VERIFIED",
    });
    expect(authAPI.post).toHaveBeenCalledWith("/login", {
      email: "user@example.com",
      password: "correct-password",
      turnstile_token: undefined,
      human_check: "human",
    });
  });
});
