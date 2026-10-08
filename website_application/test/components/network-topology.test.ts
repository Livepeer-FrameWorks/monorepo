import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { writable } from "svelte/store";
import NetworkTopology from "../../src/lib/components/NetworkTopology.svelte";

const api = vi.hoisted(() => ({ fetch: vi.fn() }));
vi.mock("$houdini", () => ({
  GetNetworkStatusStore: class {
    subscribe = writable({ data: null }).subscribe;
    fetch = api.fetch;
  },
}));
afterEach(cleanup);

describe("network topology availability", () => {
  it("shows an unavailable state when GraphQL returns errors without throwing", async () => {
    api.fetch.mockResolvedValue({ data: null, errors: [{ message: "upstream unavailable" }] });
    render(NetworkTopology, { viewport: {} });
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toContain("temporarily unavailable")
    );
  });
  it("shows an unavailable state for a rejected request", async () => {
    api.fetch.mockRejectedValue(new Error("offline"));
    render(NetworkTopology, { viewport: {} });
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toContain("temporarily unavailable")
    );
  });
});
