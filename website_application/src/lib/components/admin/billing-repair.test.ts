import { describe, expect, it } from "vitest";
import { billingAdjustmentCents, entitlementEntries } from "./billing-repair";
describe("operator billing adjustments", () => {
  it("uses exact decimal cents and explicit debit signs", () => {
    expect(billingAdjustmentCents("79.00")).toBe(7900);
    expect(billingAdjustmentCents("-9.12")).toBe(-912);
    expect(billingAdjustmentCents("0.01")).toBe(1);
  });
  it("rejects fractional cents, zero and overflow", () => {
    for (const value of ["0", "1.001", "1e3", "Infinity", "21474836.48", "-21474836.48"])
      expect(() => billingAdjustmentCents(value)).toThrow();
  });
  it("distinguishes removing overrides from zero and false values", () => {
    expect(entitlementEntries("{}")).toEqual([]);
    expect(
      entitlementEntries('{"recording_retention_days":0,"custom_domain_enabled":false}')
    ).toEqual([
      { key: "recording_retention_days", value: "0" },
      { key: "custom_domain_enabled", value: "false" },
    ]);
    for (const value of ["null", "[]", "0", '{"":1}'])
      expect(() => entitlementEntries(value)).toThrow();
  });
});
