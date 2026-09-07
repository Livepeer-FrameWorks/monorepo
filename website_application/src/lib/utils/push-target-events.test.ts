import { describe, expect, it } from "vitest";
import { shouldRefreshPushTargets } from "./push-target-events";

describe("shouldRefreshPushTargets", () => {
  it.each([
    { type: "RESTREAM_STATUS" },
    { type: "PUSH_END" },
    { type: "STREAM_LIFECYCLE_UPDATE", payload: { changed_fields: ["push_targets"] } },
    {
      type: "STREAM_LIFECYCLE_UPDATE",
      payload: JSON.stringify({ changedFields: ["push_target_status"] }),
    },
  ])("refreshes for $type push-target changes", (event) => {
    expect(shouldRefreshPushTargets(event)).toBe(true);
  });

  it.each([
    { type: "STREAM_START" },
    { type: "STREAM_LIFECYCLE_UPDATE", payload: { changed_fields: ["title"] } },
    { type: "STREAM_LIFECYCLE_UPDATE", payload: "not-json" },
  ])("ignores unrelated event %#", (event) => {
    expect(shouldRefreshPushTargets(event)).toBe(false);
  });
});
