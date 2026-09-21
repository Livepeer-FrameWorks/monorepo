import { describe, expect, it } from "vitest";
import { topupCurrency } from "./presentment-currency";

describe("top-up currency", () => {
  it("uses the billing details' presentment currency", () => {
    expect(topupCurrency({ presentmentCurrency: "USD" })).toBe("USD");
    expect(topupCurrency({ presentmentCurrency: " gbp " })).toBe("GBP");
  });

  it.each([
    ["missing billing details", null],
    ["undefined billing details", undefined],
    ["a null currency", { presentmentCurrency: null }],
    ["an empty currency", { presentmentCurrency: "" }],
    ["a malformed currency", { presentmentCurrency: "EURO" }],
  ])("has no currency for %s instead of assuming EUR", (_label, details) => {
    expect(topupCurrency(details)).toBeNull();
  });
});
