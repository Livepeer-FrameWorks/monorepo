import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import RecommendedIngest from "$lib/components/placement/RecommendedIngest.svelte";
import { resolveIngestDestination } from "$lib/placement/ingest-api";
import type { ResolvedIngest } from "$lib/placement/ingest";

vi.mock("$lib/placement/ingest-api", () => ({ resolveIngestDestination: vi.fn() }));
vi.mock("$lib/stores/auth", async () => {
  const { writable } = await import("svelte/store");
  return { auth: writable({ isAuthenticated: true, user: { id: "actor", tenant_id: "tenant" } }) };
});
const streamId = "10000000-0000-4000-8000-000000000001";
const streamKey = "secret-stream-key";
function result(): ResolvedIngest {
  return {
    primary: {
      kind: "INGEST_ENDPOINT_KIND_NODE_SPECIFIC",
      nodeId: "node",
      clusterId: "cluster",
      region: "us-east",
      baseUrl: "https://us.example:8443",
      rtmpUrl: `rtmp://us.example:1935/live/${streamKey}`,
      whipUrl: `https://us.example:8443/webrtc/${streamKey}`,
      srtUrl: `srt://us.example:8890?streamid=${streamKey}`,
    },
    metadata: { streamId },
  };
}
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(resolveIngestDestination).mockResolvedValue(result());
});
afterEach(cleanup);

describe("recommended ingest controls", () => {
  it("resolves only on request, masks credentials and splits RTMP server/key", async () => {
    render(RecommendedIngest, { streamId, streamKey });
    expect(resolveIngestDestination).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Resolve destination" }));
    await screen.findByText(/us.example:8443 · us-east/);
    expect((screen.getByLabelText("RTMP server") as HTMLInputElement).value).toBe(
      "rtmp://us.example:1935/live"
    );
    expect((screen.getByLabelText("RTMP stream key") as HTMLInputElement).type).toBe("password");
    expect((screen.getByLabelText("WHIP URL") as HTMLInputElement).type).toBe("password");
    await fireEvent.click(
      screen.getByRole("checkbox", { name: "Show credential-bearing connection URLs" })
    );
    expect((screen.getByLabelText("RTMP stream key") as HTMLInputElement).type).toBe("text");
  });

  it("clears an old recommendation on refresh failure without displaying raw error credentials", async () => {
    render(RecommendedIngest, { streamId, streamKey });
    await fireEvent.click(screen.getByRole("button", { name: "Resolve destination" }));
    await screen.findByText(/us.example:8443 · us-east/);
    vi.mocked(resolveIngestDestination).mockRejectedValue(
      new Error(`failure https://node/${streamKey}`)
    );
    await fireEvent.click(screen.getByRole("button", { name: "Refresh destination" }));
    await screen.findByText(/No generic address has been substituted/);
    expect(screen.queryByText(/us.example:8443 · us-east/)).toBeNull();
    expect(screen.getByRole("alert").textContent).not.toContain(streamKey);
  });

  it("invalidates the old node and requests the selected publisher protocol", async () => {
    render(RecommendedIngest, { streamId, streamKey });
    await fireEvent.click(screen.getByRole("button", { name: "Resolve destination" }));
    await screen.findByText(/us.example:8443 · us-east/);
    await fireEvent.change(screen.getByRole("combobox", { name: "Publisher protocol" }), {
      target: { value: "SRT" },
    });
    expect(screen.queryByText(/us.example:8443 · us-east/)).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Resolve destination" }));
    await screen.findByText(/us.example:8443 · us-east/);
    expect(vi.mocked(resolveIngestDestination).mock.calls[1][3]).toBe("SRT");
  });

  it("cancels in-flight resolution when the required protocol changes", async () => {
    let complete!: (value: ResolvedIngest) => void;
    vi.mocked(resolveIngestDestination).mockImplementation(
      () => new Promise((resolve) => (complete = resolve))
    );
    render(RecommendedIngest, { streamId, streamKey });
    await fireEvent.click(screen.getByRole("button", { name: "Resolve destination" }));
    const signal = vi.mocked(resolveIngestDestination).mock.calls[0][2];
    await fireEvent.change(screen.getByRole("combobox", { name: "Publisher protocol" }), {
      target: { value: "WHIP" },
    });
    expect(signal?.aborted).toBe(true);
    complete(result());
    await screen.findByRole("button", { name: "Resolve destination" });
    expect(screen.queryByText(/us.example:8443 · us-east/)).toBeNull();
  });

  it("cancels a pending request and ignores its result when the stream changes", async () => {
    let complete!: (value: ResolvedIngest) => void;
    vi.mocked(resolveIngestDestination).mockImplementation(
      () => new Promise((resolve) => (complete = resolve))
    );
    const component = render(RecommendedIngest, { streamId, streamKey });
    await fireEvent.click(screen.getByRole("button", { name: "Resolve destination" }));
    const signal = vi.mocked(resolveIngestDestination).mock.calls[0][2];
    await component.rerender({
      streamId: "10000000-0000-4000-8000-000000000002",
      streamKey: "new-key",
    });
    expect(signal?.aborted).toBe(true);
    complete(result());
    await screen.findByRole("button", { name: "Resolve destination" });
    expect(screen.queryByText(/us.example:8443 · us-east/)).toBeNull();
  });
});
