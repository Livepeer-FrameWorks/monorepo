import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/svelte";

vi.mock("$lib/config", () => ({
  getMarketingSiteUrl: () => "https://frameworks.example/",
}));

const { default: ComingSoonModal } = await import("$lib/components/ComingSoonModal.svelte");

afterEach(() => cleanup());

describe("coming soon modal", () => {
  it("describes cloud production as server-side work only and links the showcase pipeline", async () => {
    render(ComingSoonModal, {
      show: true,
      item: { name: "Cloud production", description: "Server-side production", icon: "Cloud" },
    });
    const body = document.body.textContent ?? "";
    expect(body).toContain("on our servers");
    expect(body).toContain("backup input");
    expect(body).not.toMatch(/overlays and scenes/i);
    const link = (await screen.findByText("See what's next")).closest("a");
    expect(link?.getAttribute("href")).toBe("https://frameworks.example/showcase#up-next");
  });
});
