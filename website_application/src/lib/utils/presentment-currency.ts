// The currency a tenant is charged in comes from billingDetails.presentmentCurrency, which
// Purser derives from the billing country. A missing or malformed value yields null so a
// page shows an error state rather than labelling amounts in a currency it guessed.
export function topupCurrency(
  details: { presentmentCurrency?: string | null } | null | undefined
): string | null {
  const code = details?.presentmentCurrency?.trim().toUpperCase() ?? "";
  return /^[A-Z]{3}$/.test(code) ? code : null;
}
