import { describe, expect, it } from "vitest";
import { clusterStatusLabel, WAITING_FOR_FIRST_EDGE } from "./cluster-status";

describe("cluster status label", () => {
  it("names a new cluster with no nodes as waiting for its first edge", () => {
    expect(clusterStatusLabel("unknown", 0)).toBe(WAITING_FOR_FIRST_EDGE);
    expect(clusterStatusLabel("", 0)).toBe(WAITING_FOR_FIRST_EDGE);
  });

  it("keeps unknown while the node count is loading or nodes exist", () => {
    expect(clusterStatusLabel("unknown", null)).toBe("unknown");
    expect(clusterStatusLabel("unknown", 2)).toBe("unknown");
  });

  it("passes a reported health through", () => {
    expect(clusterStatusLabel("healthy", 0)).toBe("healthy");
    expect(clusterStatusLabel("degraded", 3)).toBe("degraded");
  });
});
