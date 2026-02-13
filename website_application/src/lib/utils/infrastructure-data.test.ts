import { describe, expect, it } from "vitest";

import {
  filterNodePerformance,
  serviceInstanceRenderKey,
  sortServiceInstancesForRender,
} from "./infrastructure-data";

describe("infrastructure-data helpers", () => {
  it("sorts service instances by newest health check and startedAt", () => {
    const sorted = sortServiceInstancesForRender([
      {
        id: "3",
        instanceId: "decklog-1",
        startedAt: "2026-01-01T10:00:00.000Z",
        lastHealthCheck: "2026-01-01T11:00:00.000Z",
      },
      {
        id: "1",
        instanceId: "bridge-1",
        startedAt: "2026-01-01T09:00:00.000Z",
        lastHealthCheck: "2026-01-01T12:00:00.000Z",
      },
      {
        id: "2",
        instanceId: "bridge-2",
        startedAt: "2026-01-01T12:00:00.000Z",
        lastHealthCheck: "2026-01-01T12:00:00.000Z",
      },
    ]);

    expect(sorted.map((instance) => instance.id)).toEqual(["2", "1", "3"]);
  });

  it("builds stable render keys that change across restarts", () => {
    const before = serviceInstanceRenderKey({
      id: "node-A:bridge-1",
      instanceId: "bridge-1",
      startedAt: "2026-01-01T12:00:00.000Z",
    });

    const afterRestart = serviceInstanceRenderKey({
      id: "node-A:bridge-1",
      instanceId: "bridge-1",
      startedAt: "2026-01-01T12:03:00.000Z",
    });

    expect(before).not.toEqual(afterRestart);
  });

  it("filters performance samples to the selected node", () => {
    const filtered = filterNodePerformance(
      [
        { nodeId: "node-1", timestamp: "2026-01-01T12:00:00.000Z" },
        { nodeId: "node-2", timestamp: "2026-01-01T12:05:00.000Z" },
      ],
      "node-2"
    );

    expect(filtered).toHaveLength(1);
    expect(filtered[0]?.nodeId).toBe("node-2");
  });
});
