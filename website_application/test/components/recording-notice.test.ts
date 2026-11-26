import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/svelte";
const mocks = vi.hoisted(() => ({ fetch: vi.fn(), error: vi.fn(), success: vi.fn() }));
vi.mock("$houdini", () => ({
  GetStreamRecordingEventsStore: class {
    fetch = mocks.fetch;
  },
}));
vi.mock("$lib/stores/toast.js", () => ({ toast: { error: mocks.error, success: mocks.success } }));
const { default: RecordingNotice } =
  await import("$lib/components/stream-details/RecordingNotice.svelte");
beforeEach(() => {
  sessionStorage.clear();
  mocks.fetch.mockReset();
  mocks.error.mockReset();
  mocks.success.mockReset();
});
afterEach(cleanup);
function blocked() {
  mocks.fetch.mockResolvedValue({
    data: {
      analytics: {
        lifecycle: {
          recordingEventsConnection: {
            edges: [
              {
                node: {
                  type: "RECORDING_REFUSED",
                  timestamp: "2026-11-26T10:00:01Z",
                  details: "Storage limit reached",
                },
              },
            ],
          },
        },
      },
    },
  });
}
describe("persistent recording notice", () => {
  it("restores a storage refusal on reload and keeps it after broadcast end", async () => {
    blocked();
    render(RecordingNotice, {
      streamId: "stream",
      enabled: true,
      isLive: true,
      connectedAt: "2026-11-26T10:00:00Z",
    });
    expect(await screen.findByText("Automatic recording could not start")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Review storage" }).getAttribute("href")).toBe(
      "/account/storage"
    );
    expect(screen.getByRole("alert").textContent?.replace(/\s+/g, " ")).toContain("Footage missed");
    cleanup();
    render(RecordingNotice, { streamId: "stream", enabled: true, isLive: false });
    expect(await screen.findByText("The last broadcast had a recording problem")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Review billing" })).toBeTruthy();
  });
  it("never reports capture when recording history is unavailable", async () => {
    mocks.fetch.mockRejectedValue(new Error("unavailable"));
    render(RecordingNotice, { streamId: "stream", enabled: true, isLive: true });
    expect(await screen.findByText(/Recording status is unavailable/)).toBeTruthy();
    expect(screen.queryByText("Recording is capturing media.")).toBeNull();
  });
});
