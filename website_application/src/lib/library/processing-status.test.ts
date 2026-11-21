import { describe, expect, it } from "vitest";
import { processingStatusLabel } from "./processing-status";

describe("processing queue status", () => {
  it.each([
    ["memory", "memory"],
    ["slots", "processing slots"],
    ["fitting_node_unavailable", "suitable node unavailable"],
    ["cluster_unavailable", "cluster unavailable"],
  ])("shows why work is queued: %s", (reason, label) => {
    expect(processingStatusLabel("processing", false, reason)).toBe(`Queued (${label})`);
  });

  it("keeps terminal state authoritative over a stale reason", () => {
    expect(processingStatusLabel("ready", false, "memory")).toBe("ready");
    expect(processingStatusLabel("failed", false, "slots")).toBe("failed");
    expect(processingStatusLabel("processing", true, "memory")).toBe("Expired");
  });

  it("handles future reasons without showing raw protocol codes", () => {
    expect(processingStatusLabel("queued", false, "new_reason")).toBe("Queued");
    expect(processingStatusLabel("processing", false, null)).toBe("processing");
  });
});
