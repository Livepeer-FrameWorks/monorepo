import { describe, expect, it } from "vitest";
import { sumWork, workAmount, workMinutes } from "../../packages/map-core/network-work";

import { workFixture } from "./fixtures/network-work";

describe("network work accounting", () => {
  it("keeps pixels above JavaScript precision and combines exact fractional charges", () => {
    const total = sumWork([
      workFixture(),
      workFixture({
        ip: "192.0.2.2",
        accepted_source_ms: "0",
        accepted_segments: 0,
        service_charge: { numerator: "2", denominator: "3", currency: "ETH", unit: "wei" },
      }),
    ])!;
    expect(total.returned_pixels).toBe("18014398509481986");
    expect(total.accepted_source_ms).toBe("60000");
    expect(total.service_charge).toEqual({
      numerator: "1",
      denominator: "1",
      currency: "ETH",
      unit: "wei",
    });
    expect(total.ticket_credit_sent).toEqual({
      numerator: "7",
      denominator: "1",
      currency: "ETH",
      unit: "wei",
    });
  });
  it("distinguishes absent money and observations from known zero", () => {
    expect(sumWork([])).toBeUndefined();
    expect(workAmount()).toBe("Not observed");
    expect(workAmount({ numerator: "0", denominator: "1", currency: "ETH", unit: "wei" })).toBe(
      "0 ETH"
    );
    expect(workAmount({ numerator: "1", denominator: "3", currency: "ETH", unit: "wei" })).toBe(
      "<1 wei"
    );
    expect(workMinutes("60000")).toBe("1.00");
    expect(
      workAmount({ numerator: "1000000000000000", denominator: "1", currency: "ETH", unit: "wei" })
    ).toBe("0.001 ETH");
  });
});
