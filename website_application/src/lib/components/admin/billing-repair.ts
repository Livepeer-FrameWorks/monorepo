export function billingAdjustmentCents(amount: string): number {
  const match = /^(-?)(\d+)(?:\.(\d{1,2}))?$/.exec(amount.trim());
  if (!match) throw new Error("Use an EUR amount with at most two decimal places.");
  const magnitude = Number(match[2]) * 100 + Number((match[3] ?? "").padEnd(2, "0"));
  const cents = match[1] ? -magnitude : magnitude;
  if (!Number.isSafeInteger(cents) || cents === 0 || Math.abs(cents) > 2_147_483_647)
    throw new Error("Enter a non-zero EUR amount within the adjustment limit.");
  return cents;
}

export function entitlementEntries(value: string): { key: string; value: string }[] {
  const parsed: unknown = JSON.parse(value);
  if (parsed === null || Array.isArray(parsed) || typeof parsed !== "object")
    throw new Error("Enter an object mapping entitlement keys to JSON values.");
  return Object.entries(parsed).map(([key, entry]) => {
    if (!key.trim()) throw new Error("Entitlement keys cannot be empty.");
    return { key, value: JSON.stringify(entry) };
  });
}
