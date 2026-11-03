import { describe, expect, it } from "vitest";
import { invoiceLineAmountDisplay } from "./invoice-line-display";

describe("invoiceLineAmountDisplay", () => {
  it("names the operator grant for grant_waived lines instead of Included", () => {
    const display = invoiceLineAmountDisplay(0, "grant_waived");
    expect(display.kind).toBe("waived");
    expect(display).toMatchObject({ badge: "Waived by arrangement" });
  });

  it("shows the platform waiver for beta_free lines", () => {
    expect(invoiceLineAmountDisplay(0, "beta_free")).toEqual({
      kind: "waived",
      badge: "Waived",
      title: "Usage charges waived",
    });
  });

  it("keeps the two waiver wordings distinct", () => {
    const grant = invoiceLineAmountDisplay(0, "grant_waived");
    const beta = invoiceLineAmountDisplay(0, "beta_free");
    expect(grant).not.toEqual(beta);
  });

  it("marks other zero-total lines as included", () => {
    expect(invoiceLineAmountDisplay(0, "included_subscription")).toEqual({
      kind: "included",
      badge: "Included",
    });
    expect(invoiceLineAmountDisplay(0, "free_unmetered").kind).toBe("included");
  });

  it("shows the amount for charged lines", () => {
    expect(invoiceLineAmountDisplay(12.5, "cluster_metered")).toEqual({ kind: "amount" });
    expect(invoiceLineAmountDisplay(3, null)).toEqual({ kind: "amount" });
  });
});
