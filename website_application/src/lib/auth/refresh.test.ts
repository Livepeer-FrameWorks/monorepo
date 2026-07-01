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

async function loadModule() {
  vi.doMock("$app/environment", () => ({ browser: true }));
  return import("./refresh");
}

describe("refreshAuthSession", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.stubEnv("VITE_AUTH_URL", "http://auth.test");
    installLocalStorage();
  });

  it("returns ok and stores the user on a successful refresh", async () => {
    const fetchFn = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({ user: { id: "user-1" } }), { status: 200 }));

    const { refreshAuthSession } = await loadModule();

    await expect(refreshAuthSession(fetchFn)).resolves.toBe("ok");
    expect(fetchFn).toHaveBeenCalledWith(
      "http://auth.test/refresh",
      expect.objectContaining({ method: "POST", credentials: "include" })
    );
    expect(localStorage.setItem).toHaveBeenCalledWith("user", JSON.stringify({ id: "user-1" }));
  });

  it("defaults to the same-origin gateway auth prefix", async () => {
    vi.unstubAllEnvs();
    const fetchFn = vi.fn().mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));

    const { refreshAuthSession } = await loadModule();

    await expect(refreshAuthSession(fetchFn)).resolves.toBe("ok");
    expect(fetchFn).toHaveBeenCalledWith(
      "/auth/refresh",
      expect.objectContaining({ method: "POST", credentials: "include" })
    );
  });

  it("classifies a 401 as unauthorized", async () => {
    const fetchFn = vi.fn().mockResolvedValue(new Response("", { status: 401 }));

    const { refreshAuthSession } = await loadModule();

    await expect(refreshAuthSession(fetchFn)).resolves.toBe("unauthorized");
  });

  it("classifies 5xx responses as transient", async () => {
    const fetchFn = vi.fn().mockResolvedValue(new Response("", { status: 503 }));

    const { refreshAuthSession } = await loadModule();

    await expect(refreshAuthSession(fetchFn)).resolves.toBe("transient");
  });

  it("classifies network failures as transient", async () => {
    const fetchFn = vi.fn().mockRejectedValue(new TypeError("fetch failed"));

    const { refreshAuthSession } = await loadModule();

    await expect(refreshAuthSession(fetchFn)).resolves.toBe("transient");
  });

  it("shares one in-flight request between concurrent callers", async () => {
    let release: (response: Response) => void = () => {};
    const fetchFn = vi.fn().mockReturnValue(
      new Promise<Response>((resolve) => {
        release = resolve;
      })
    );

    const { refreshAuthSession } = await loadModule();

    const first = refreshAuthSession(fetchFn);
    const second = refreshAuthSession(fetchFn);
    release(new Response(JSON.stringify({}), { status: 200 }));

    await expect(Promise.all([first, second])).resolves.toEqual(["ok", "ok"]);
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  it("serializes the refresh through the Web Locks API when available", async () => {
    const request = vi.fn(async (_name: string, callback: () => Promise<unknown>) => callback());
    vi.stubGlobal("navigator", { locks: { request } });

    const fetchFn = vi.fn().mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));

    const { refreshAuthSession } = await loadModule();

    await expect(refreshAuthSession(fetchFn)).resolves.toBe("ok");
    expect(request).toHaveBeenCalledWith("fw-auth-refresh", expect.any(Function));
  });
});
