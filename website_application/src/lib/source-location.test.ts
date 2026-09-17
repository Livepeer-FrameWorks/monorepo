import { describe, expect, it } from "vitest";
import {
  describeSourceLocation,
  draftFromSourceLocation,
  offeredClusters,
  sameSourceLocation,
  sourceLocationInput,
  sourceLocationProblem,
} from "./source-location";

const clusters = [
  { clusterId: "home", clusterName: "Home", accessLevel: "owner", allowPrivatePullSources: true },
  {
    clusterId: "market",
    clusterName: "Market",
    accessLevel: "subscriber",
    allowPrivatePullSources: false,
  },
];

describe("source location model", () => {
  it("treats CUSTOM as not editable", () => {
    expect(draftFromSourceLocation({ mode: "CUSTOM", clusters: [], avoidNodeIds: [] })).toBeNull();
    expect(draftFromSourceLocation(null)).toEqual({ mode: "ANY", clusters: [], avoidNodeIds: [] });
  });

  it("compares locations independent of order and clears ANY details", () => {
    expect(
      sameSourceLocation(
        {
          mode: "RESTRICTED",
          clusters: [
            { clusterId: "a", nodeIds: ["n2", "n1"] },
            { clusterId: "b", nodeIds: [] },
          ],
          avoidNodeIds: ["x", "y"],
        },
        {
          mode: "RESTRICTED",
          clusters: [
            { clusterId: "b", nodeIds: [] },
            { clusterId: "a", nodeIds: ["n1", "n2"] },
          ],
          avoidNodeIds: ["y", "x"],
        }
      )
    ).toBe(true);
    expect(
      sourceLocationInput({
        mode: "ANY",
        clusters: [{ clusterId: "a", nodeIds: [] }],
        avoidNodeIds: ["x"],
      })
    ).toEqual({ mode: "ANY", clusters: [], avoidNodeIds: [] });
  });

  it("hides clusters without private-pull consent for private sources", () => {
    expect(offeredClusters(clusters, "public").hidden).toBe(0);
    const result = offeredClusters(clusters, "private");
    expect(result.offered.map((cluster) => cluster.clusterId)).toEqual(["home"]);
    expect(result.hidden).toBe(1);
  });

  it("reports problems the server would reject", () => {
    const any = { mode: "ANY" as const, clusters: [], avoidNodeIds: [] };
    expect(sourceLocationProblem(any, "public", clusters)).toBeNull();
    expect(sourceLocationProblem(any, "private", clusters)).toMatch(/need specific clusters/);
    expect(
      sourceLocationProblem({ mode: "RESTRICTED", clusters: [], avoidNodeIds: [] }, "public", [])
    ).toBe("Choose at least one cluster.");
    expect(
      sourceLocationProblem(
        { mode: "RESTRICTED", clusters: [{ clusterId: "market", nodeIds: [] }], avoidNodeIds: [] },
        "private",
        clusters
      )
    ).toBe("Market does not allow private pull sources.");
  });

  it("describes each mode", () => {
    expect(describeSourceLocation(null)).toBe("Any cluster FrameWorks chooses");
    expect(
      describeSourceLocation({
        mode: "RESTRICTED",
        clusters: [{ clusterId: "home", nodeIds: ["n1"] }],
        avoidNodeIds: ["n2", "n3"],
      })
    ).toBe("Only home (1 node), avoiding 2 nodes");
  });
});
