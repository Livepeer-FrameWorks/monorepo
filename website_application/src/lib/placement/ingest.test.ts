import { describe, expect, it } from "vitest";
import {
  destinationHost,
  ingestProtocolUrl,
  isSpecificIngest,
  splitRtmpDestination,
  type IngestDestination,
} from "./ingest";

function destination(): IngestDestination {
  return {
    nodeId: "node",
    clusterId: "cluster",
    kind: "INGEST_ENDPOINT_KIND_NODE_SPECIFIC",
    baseUrl: "https://edge.example:8443",
    region: "us-east",
    rtmpUrl: "rtmp://edge.example:1935/live/key%2Fwith%20space",
    srtUrl: "srt://edge.example:8890?streamid=secret",
    whipUrl: "https://edge.example:8443/webrtc/secret",
  };
}

describe("resolved ingest presentation", () => {
  it("requires an actual destination identity, not a generic entry", () => {
    expect(isSpecificIngest(destination())).toBe(true);
    expect(isSpecificIngest({ ...destination(), kind: "INGEST_ENDPOINT_KIND_ROOT_POOL" })).toBe(
      false
    );
    expect(isSpecificIngest({ ...destination(), clusterId: "" })).toBe(false);
    expect(isSpecificIngest({ ...destination(), nodeId: "" })).toBe(false);
  });

  it("shows only the public authority and preserves actual advertised ports", () => {
    expect(destinationHost(destination())).toBe("edge.example:8443");
    expect(
      destinationHost({ ...destination(), baseUrl: "https://user:secret@edge.example" })
    ).toBeNull();
    expect(ingestProtocolUrl(destination(), "srt")).toBe("srt://edge.example:8890?streamid=secret");
  });

  it("separates OBS server/key only for an exact terminal credential segment", () => {
    expect(splitRtmpDestination(destination(), "key/with space")).toEqual({
      server: "rtmp://edge.example:1935/live",
      key: "key/with space",
    });
    expect(splitRtmpDestination(destination(), "other-key")).toBeNull();
    expect(
      splitRtmpDestination(
        { ...destination(), rtmpUrl: "rtmp://edge.example/live/key?token=secret" },
        "key"
      )
    ).toBeNull();
    expect(
      splitRtmpDestination({ ...destination(), rtmpUrl: "rtmp://edge.example/live/%ZZ" }, "key")
    ).toBeNull();
  });

  it("rejects malformed or wrong-protocol URLs without constructing replacements", () => {
    for (const value of [
      "javascript:alert(1)",
      "//edge.example/webrtc/key",
      "https://user:secret@edge.example/key",
      "https://edge.example/key#secret",
    ]) {
      expect(ingestProtocolUrl({ ...destination(), whipUrl: value }, "whip")).toBeNull();
    }
    expect(ingestProtocolUrl({ ...destination(), rtmpUrl: null }, "rtmp")).toBeNull();
    expect(
      ingestProtocolUrl({ ...destination(), rtmpUrl: "https://edge.example/key" }, "rtmp")
    ).toBeNull();
  });
});
