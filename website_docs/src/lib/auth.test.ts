import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const user = {
  id: "user-1",
  email: "viewer@example.com",
  display_name: "Viewer",
  tenant_id: "tenant-1",
};

async function loadAuth() {
  return import("./auth");
}

beforeEach(() => {
  vi.resetModules();
  delete (globalThis as typeof globalThis & { __frameworksDocsAuth?: unknown })
    .__frameworksDocsAuth;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("docs auth coordinator", () => {
  it("coalesces concurrent authentication checks", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(user), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    );
    vi.stubGlobal("fetch", fetchMock);
    const { checkAuth } = await loadAuth();

    const results = await Promise.all([checkAuth(), checkAuth(), checkAuth()]);

    expect(results).toEqual([user, user, user]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toMatch(/\/auth\/me$/);
  });

  it("performs one refresh for a coalesced unauthorized check", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ user }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      );
    vi.stubGlobal("fetch", fetchMock);
    const { checkAuth } = await loadAuth();

    await Promise.all([checkAuth(), checkAuth()]);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1]?.[0]).toMatch(/\/auth\/refresh$/);
  });

  it("publishes login and logout state to every subscriber", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ user }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    const { login, logout, subscribeAuth } = await loadAuth();
    const first: Array<string | null> = [];
    const second: Array<string | null> = [];
    const unsubscribeFirst = subscribeAuth((state) => first.push(state.user?.id ?? null));
    const unsubscribeSecond = subscribeAuth((state) => second.push(state.user?.id ?? null));

    await login("viewer@example.com", "secret");
    await logout();
    unsubscribeFirst();
    unsubscribeSecond();

    expect(first).toEqual([null, user.id, null]);
    expect(second).toEqual(first);
  });
});
