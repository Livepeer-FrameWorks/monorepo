export interface NodeCapacity {
  egressBudgetBytesPerSec: number;
  ingressBudgetBytesPerSec: number;
  egressSource: string;
  ingressSource: string;
  revision: number;
  appliedRevision: number | null;
}

export function budgetFromMbps(value: string): number | null {
  if (!value.trim()) return null;
  const bytes = Number(value) * 125_000;
  if (!Number.isSafeInteger(bytes) || bytes <= 0) {
    throw new Error(
      "Enter positive Mbit/s that converts to whole bytes per second, or leave blank for default."
    );
  }
  return bytes;
}

export function budgetInput(bytes: number, source: string): string {
  return source === "DEFAULT" ? "" : String(bytes / 125_000);
}
