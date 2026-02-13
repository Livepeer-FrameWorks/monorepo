export interface QueryHistoryItem {
  id: number;
  query: string;
  variables: Record<string, unknown>;
  result: unknown;
  timestamp: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

export function normalizeQueryHistory(input: unknown): QueryHistoryItem[] {
  if (!Array.isArray(input)) {
    return [];
  }

  return input
    .filter((item): item is QueryHistoryItem => {
      if (!isRecord(item)) return false;

      return (
        typeof item.id === "number" &&
        typeof item.query === "string" &&
        isRecord(item.variables) &&
        typeof item.timestamp === "string"
      );
    })
    .slice(0, 10);
}
