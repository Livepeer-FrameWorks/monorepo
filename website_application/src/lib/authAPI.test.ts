import { beforeEach, describe, expect, it, vi } from "vitest";

async function loadAuthAPI() {
  vi.resetModules();
  return import("./authAPI");
}

describe("authAPI configuration", () => {
  beforeEach(() => {
    vi.unstubAllEnvs();
  });

  it("defaults to the same-origin gateway auth prefix", async () => {
    const { AUTH_URL, authAPI } = await loadAuthAPI();

    expect(AUTH_URL).toBe("/auth");
    expect(authAPI.defaults.baseURL).toBe("/auth");
  });

  it("uses VITE_AUTH_URL when configured", async () => {
    vi.stubEnv("VITE_AUTH_URL", "https://bridge.example.com/auth");

    const { AUTH_URL, authAPI } = await loadAuthAPI();

    expect(AUTH_URL).toBe("https://bridge.example.com/auth");
    expect(authAPI.defaults.baseURL).toBe("https://bridge.example.com/auth");
  });
});
