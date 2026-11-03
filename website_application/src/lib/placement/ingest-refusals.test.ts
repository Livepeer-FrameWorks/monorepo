import { describe, expect, it } from "vitest";
import { BALANCE_REFUSAL, describeIngestRefusal } from "./ingest-refusals";

describe("describeIngestRefusal", () => {
  it("explains an ended operator grant apart from a spent balance", () => {
    const refusal = describeIngestRefusal("GRANT_EXPIRED");

    expect(refusal).not.toBeNull();
    expect(refusal).not.toBe(BALANCE_REFUSAL);
    expect(refusal!.title).toMatch(/ended/);
    expect(refusal!.actions).toContain("billing");
  });

  it("has no guidance for the generic denial or an unknown reason", () => {
    expect(describeIngestRefusal("INGEST_DENIED")).toBeNull();
    expect(describeIngestRefusal("NOT_A_REASON")).toBeNull();
    expect(describeIngestRefusal("toString")).toBeNull();
    expect(describeIngestRefusal(null)).toBeNull();
  });
});
