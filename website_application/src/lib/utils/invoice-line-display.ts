// How the amount cell of an invoice line renders. Purser stamps a usage line
// waived to zero with beta_free (the platform waiver) or grant_waived (the
// tenant's operator grant); any other zero-total line is included in the plan.
export type InvoiceLineAmountDisplay =
  | { kind: "amount" }
  | { kind: "included"; badge: string }
  | { kind: "waived"; badge: string; title: string };

export const PLATFORM_WAIVER_SOURCE = "beta_free";
export const GRANT_WAIVER_SOURCE = "grant_waived";

export function invoiceLineAmountDisplay(
  total: number,
  pricingSource: string | null | undefined
): InvoiceLineAmountDisplay {
  if (total !== 0) {
    return { kind: "amount" };
  }
  if (pricingSource === GRANT_WAIVER_SOURCE) {
    return {
      kind: "waived",
      badge: "Waived by arrangement",
      title: "Usage waived under your FrameWorks arrangement",
    };
  }
  if (pricingSource === PLATFORM_WAIVER_SOURCE) {
    // Lines rated before grant_waived existed carry beta_free for a grant
    // waiver too, so this wording does not name the beta.
    return { kind: "waived", badge: "Waived", title: "Usage charges waived" };
  }
  return { kind: "included", badge: "Included" };
}
