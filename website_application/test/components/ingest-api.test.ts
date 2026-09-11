import { beforeEach, describe, expect, it, vi } from "vitest";
import { resolveIngestDestination } from "$lib/placement/ingest-api";

const { fetchDestination } = vi.hoisted(() => ({ fetchDestination: vi.fn() }));
vi.mock("$houdini", () => ({
  ResolveIngestDestinationStore: class {
    fetch = fetchDestination;
  },
}));

const streamId = "10000000-0000-4000-8000-000000000001";
const primary = {
  nodeId: "node",
  clusterId: "cluster",
  kind: "INGEST_ENDPOINT_KIND_NODE_SPECIFIC",
  baseUrl: "https://node.example",
  whipUrl: "https://node.example/webrtc/key",
  rtmpUrl: "rtmp://node.example:2935/live/key",
  srtUrl: "srt://node.example:18889/?streamid=key",
};

beforeEach(() => {
  fetchDestination.mockReset();
  fetchDestination.mockResolvedValue({
    data: { resolveIngestEndpoint: { primary, metadata: { streamId } } },
  });
});

describe("protocol-specific ingest requests", () => {
  it.each(["WHIP", "RTMP", "SRT"] as const)(
    "requests %s before node selection",
    async (protocol) => {
      await resolveIngestDestination(streamId, "key", undefined, protocol);
      expect(fetchDestination).toHaveBeenCalledWith(
        expect.objectContaining({
          variables: { streamKey: "key", protocol },
          policy: "NetworkOnly",
        })
      );
    }
  );

  it("rejects a response that only supports a different protocol", async () => {
    fetchDestination.mockResolvedValue({
      data: {
        resolveIngestEndpoint: {
          primary: { ...primary, whipUrl: null },
          metadata: { streamId },
        },
      },
    });
    await expect(resolveIngestDestination(streamId, "key", undefined, "WHIP")).rejects.toThrow(
      "No generic address has been substituted"
    );
  });

  it("keeps unspecified protocol discovery available for manual setup", async () => {
    await resolveIngestDestination(streamId, "key");
    expect(fetchDestination).toHaveBeenCalledWith(
      expect.objectContaining({ variables: { streamKey: "key", protocol: undefined } })
    );
  });
});
