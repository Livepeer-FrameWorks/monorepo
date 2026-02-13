import { describe, expect, it } from "vitest";

import { buildBucketFlows, isCrossClusterEvent } from "./routingFlows";

describe("isCrossClusterEvent", () => {
  it("treats remote cluster mismatch as cross-cluster", () => {
    expect(
      isCrossClusterEvent({
        clusterId: "cluster-a",
        remoteClusterId: "cluster-b",
        status: "success",
      })
    ).toBe(true);
  });

  it("falls back to status when remote cluster id is absent", () => {
    expect(isCrossClusterEvent({ status: "remote_redirect" })).toBe(true);
    expect(isCrossClusterEvent({ status: "cross_cluster_dtsc" })).toBe(true);
  });
});

describe("buildBucketFlows", () => {
  it("splits local and cross traffic for the same bucket pair", () => {
    const flows = buildBucketFlows([
      {
        clientBucket: { h3Index: "100" },
        nodeBucket: { h3Index: "200" },
        routingDistance: 100,
        clusterId: "cluster-a",
      },
      {
        clientBucket: { h3Index: "100" },
        nodeBucket: { h3Index: "200" },
        routingDistance: 300,
        status: "remote_redirect",
      },
    ]);

    expect(flows).toHaveLength(2);

    const local = flows.find((f) => f.crossCluster === false);
    const cross = flows.find((f) => f.crossCluster === true);

    expect(local).toMatchObject({ from: "100", to: "200", count: 1, avgDistance: 100 });
    expect(cross).toMatchObject({ from: "100", to: "200", count: 1, avgDistance: 300 });
  });
});
