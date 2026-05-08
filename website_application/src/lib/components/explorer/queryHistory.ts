export interface QueryHistoryItem {
  id: number;
  query: string;
  variables: Record<string, unknown>;
  result: unknown;
  timestamp: string;
}

const REDACTED_VALUE = "[redacted]";
const SENSITIVE_KEY_PATTERN =
  /(^|_)(api_key|stream_key|key_value|auth|authorization|bearer|cookie|password|private_key|secret|token|token_value|source_uri)($|_)/i;
const JWT_VALUE_PATTERN = /^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function maybeRedactJsonString(value: string): string {
  const trimmed = value.trim();
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) {
    return JWT_VALUE_PATTERN.test(trimmed) ? REDACTED_VALUE : value;
  }

  try {
    return JSON.stringify(redactSensitiveHistoryData(JSON.parse(value)), null, 2);
  } catch {
    return JWT_VALUE_PATTERN.test(trimmed) ? REDACTED_VALUE : value;
  }
}

function normalizeHistoryKey(key: string): string {
  return key
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/[-\s]+/g, "_")
    .toLowerCase();
}

export function redactSensitiveHistoryData(value: unknown): unknown {
  if (typeof value === "string") {
    return maybeRedactJsonString(value);
  }

  if (Array.isArray(value)) {
    return value.map((item) => redactSensitiveHistoryData(item));
  }

  if (!isRecord(value)) {
    return value;
  }

  return Object.fromEntries(
    Object.entries(value).map(([key, entry]) => [
      key,
      SENSITIVE_KEY_PATTERN.test(normalizeHistoryKey(key))
        ? REDACTED_VALUE
        : redactSensitiveHistoryData(entry),
    ])
  );
}

export function redactQueryHistoryItem(item: QueryHistoryItem): QueryHistoryItem {
  return {
    ...item,
    variables: redactSensitiveHistoryData(item.variables) as Record<string, unknown>,
    result: redactSensitiveHistoryData(item.result),
  };
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
