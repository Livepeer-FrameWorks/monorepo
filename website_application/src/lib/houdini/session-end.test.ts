import { beforeEach, describe, expect, it, vi } from "vitest";
import { writable } from "svelte/store";

const state = writable({ isAuthenticated: true });
const logout = vi.fn(async () => {
  state.set({ isAuthenticated: false });
});
vi.mock("$app/environment", () => ({ browser: true }));
vi.mock("$lib/stores/auth", () => ({ auth: { subscribe: state.subscribe, logout } }));

const values = new Map<string, string>();
vi.stubGlobal("sessionStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});

const { endSession, takeSessionEnd } = await import("./session-end");

beforeEach(() => {
  values.clear();
  logout.mockClear();
});

describe("endSession", () => {
  it("signs a signed-in tab out once and leaves the reason for the login page", async () => {
    state.set({ isAuthenticated: true });
    await Promise.all([endSession("deactivated"), endSession("deactivated")]);
    expect(logout).toHaveBeenCalledTimes(1);
    expect(takeSessionEnd()).toBe("deactivated");
    expect(takeSessionEnd()).toBeNull();
  });

  it("leaves an anonymous visitor alone", async () => {
    state.set({ isAuthenticated: false });
    await endSession("expired");
    expect(logout).not.toHaveBeenCalled();
    expect(takeSessionEnd()).toBeNull();
  });
});
