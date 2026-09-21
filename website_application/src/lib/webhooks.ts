import type {
  WebhookDeliveryAttemptFields$data,
  WebhookDeliveryFields$data,
  WebhookEndpointFields$data,
} from "$houdini";

export type WebhookEndpointRow = WebhookEndpointFields$data;
export type WebhookDeliveryRow = WebhookDeliveryFields$data;
export type WebhookAttemptRow = WebhookDeliveryAttemptFields$data;

export type WebhookDeliveryStatus = "PENDING" | "SUCCEEDED" | "FAILED" | "SKIPPED";

/** Subscribes an endpoint to every public event type. */
export const ALL_EVENT_TYPES = "*";

/** A tenant can have at most this many endpoints; Bosun enforces it. */
export const MAX_WEBHOOK_ENDPOINTS = 10;

/** Bosun replays at most this many deliveries per range replay call. */
export const RANGE_REPLAY_BATCH = 1000;

export const webhookDeliveryStatuses: { value: WebhookDeliveryStatus; label: string }[] = [
  { value: "PENDING", label: "Pending" },
  { value: "SUCCEEDED", label: "Succeeded" },
  { value: "FAILED", label: "Failed" },
  { value: "SKIPPED", label: "Skipped" },
];

export function deliveryStatusLabel(status: string): string {
  return webhookDeliveryStatuses.find((item) => item.value === status)?.label ?? status;
}

export function deliveryStatusClass(status: string): string {
  switch (status) {
    case "SUCCEEDED":
      return "border-success/60 text-success";
    case "FAILED":
      return "border-destructive/60 text-destructive";
    case "PENDING":
      return "border-warning/60 text-warning";
    default:
      return "border-border text-muted-foreground";
  }
}

/** Badge label and classes for an endpoint's state, including the auto-disable reason. */
export function endpointStatusBadge(
  endpoint: Pick<WebhookEndpointRow, "status" | "disabledReason">
): {
  label: string;
  className: string;
} {
  if (endpoint.status === "ENABLED") {
    return { label: "Enabled", className: "border-success/60 text-success" };
  }
  if (endpoint.disabledReason === "FAILING") {
    return { label: "Disabled: failing", className: "border-destructive/60 text-destructive" };
  }
  return { label: "Disabled", className: "border-border text-muted-foreground" };
}

/** One line on delivery health: failure streak, or the last success. */
export function endpointHealthSummary(
  endpoint: Pick<
    WebhookEndpointRow,
    "consecutiveFailures" | "failingSince" | "lastSuccessAt" | "lastFailureAt"
  >
): string {
  if (endpoint.consecutiveFailures > 0) {
    const attempts =
      endpoint.consecutiveFailures === 1
        ? "1 failed attempt"
        : `${endpoint.consecutiveFailures} failed attempts`;
    return endpoint.failingSince
      ? `${attempts} since ${formatWebhookTime(endpoint.failingSince)}`
      : attempts;
  }
  if (endpoint.lastSuccessAt) return `Last success ${formatWebhookTime(endpoint.lastSuccessAt)}`;
  return "No deliveries yet";
}

export function eventTypesSummary(types: readonly string[]): string {
  if (types.includes(ALL_EVENT_TYPES)) return "All event types";
  if (types.length === 0) return "No event types";
  if (types.length <= 3) return types.join(", ");
  return `${types.slice(0, 3).join(", ")} +${types.length - 3} more`;
}

/**
 * Toggles one type in an event-type selection. "*" is exclusive: choosing it
 * replaces every specific type, and choosing a specific type drops "*".
 */
export function toggleEventType(selected: readonly string[], type: string): string[] {
  if (selected.includes(type)) return selected.filter((item) => item !== type);
  if (type === ALL_EVENT_TYPES) return [ALL_EVENT_TYPES];
  return [...selected.filter((item) => item !== ALL_EVENT_TYPES), type];
}

const errorClassLabels: Record<string, string> = {
  http_status: "Non-2xx response",
  redirect: "Redirect (not followed)",
  timeout: "Timed out",
  connection: "Connection failed",
  tls: "TLS handshake failed",
  dns: "DNS lookup failed",
  blocked_destination: "Blocked destination",
  secret_unavailable: "Signing secret unavailable",
};

/** Human description of an attempt's outcome from its status code and error class. */
export function attemptOutcome(statusCode: number, errorClass: string): string {
  if (!errorClass) return statusCode > 0 ? `HTTP ${statusCode}` : "Succeeded";
  const label = errorClassLabels[errorClass] ?? errorClass;
  return statusCode > 0 ? `${label} (HTTP ${statusCode})` : label;
}

export function formatWebhookTime(value: string | null | undefined): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

/**
 * Converts a datetime-local input value (local wall time, no zone) to an RFC
 * 3339 UTC timestamp. Empty or unparseable input is null.
 */
export function localInputToIso(value: string | null | undefined): string | null {
  if (!value || !value.trim()) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  return date.toISOString();
}

/** The inverse of localInputToIso, for prefilling datetime-local inputs. */
export function isoToLocalInput(value: string | Date): string {
  const date = typeof value === "string" ? new Date(value) : value;
  if (Number.isNaN(date.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}`
  );
}

export interface DeliveryFilter {
  statuses: readonly WebhookDeliveryStatus[];
  eventType: string;
  eventId: string;
  /** datetime-local input values. */
  createdAfter: string;
  createdBefore: string;
}

export const emptyDeliveryFilter: DeliveryFilter = {
  statuses: [],
  eventType: "",
  eventId: "",
  createdAfter: "",
  createdBefore: "",
};

export function deliveryFilterActive(filter: DeliveryFilter): boolean {
  return (
    filter.statuses.length > 0 ||
    filter.eventType.trim() !== "" ||
    filter.eventId.trim() !== "" ||
    filter.createdAfter !== "" ||
    filter.createdBefore !== ""
  );
}

export interface DeliveryQueryVariables {
  endpointId: string;
  statuses: WebhookDeliveryStatus[] | null;
  eventType: string | null;
  eventId: string | null;
  createdAfter: string | null;
  createdBefore: string | null;
}

/** GetWebhookDeliveriesConnection variables for one endpoint's log; blank filters are omitted. */
export function deliveryQueryVariables(
  endpointId: string,
  filter: DeliveryFilter
): DeliveryQueryVariables {
  return {
    endpointId,
    statuses: filter.statuses.length > 0 ? [...filter.statuses] : null,
    eventType: filter.eventType.trim() || null,
    eventId: filter.eventId.trim() || null,
    createdAfter: localInputToIso(filter.createdAfter),
    createdBefore: localInputToIso(filter.createdBefore),
  };
}

export type RangeReplayPlan =
  | { ok: true; variables: { endpointId: string; createdAfter: string; createdBefore: string } }
  | { ok: false; message: string };

/** Validates a range replay window given as datetime-local values. */
export function rangeReplayPlan(
  endpointId: string,
  after: string,
  before: string
): RangeReplayPlan {
  const createdAfter = localInputToIso(after);
  const createdBefore = localInputToIso(before);
  if (!createdAfter || !createdBefore) {
    return { ok: false, message: "Choose both the start and the end of the range." };
  }
  if (new Date(createdAfter).getTime() >= new Date(createdBefore).getTime()) {
    return { ok: false, message: "The start of the range must be before its end." };
  }
  return { ok: true, variables: { endpointId, createdAfter, createdBefore } };
}

export function rangeReplayMessage(replayedCount: number, hasMore: boolean): string {
  const count =
    replayedCount === 1 ? "1 delivery queued again" : `${replayedCount} deliveries queued again`;
  return hasMore
    ? `${count}. More failed or skipped deliveries remain in the range; replay again to continue.`
    : `${count}.`;
}

export interface EndpointDraft {
  url: string;
  description: string;
  eventTypes: readonly string[];
}

export function draftFromEndpoint(endpoint: WebhookEndpointRow): EndpointDraft {
  return {
    url: endpoint.url,
    description: endpoint.description,
    eventTypes: [...endpoint.eventTypes],
  };
}

export function draftProblem(draft: EndpointDraft): string | null {
  const url = draft.url.trim();
  if (!url) return "Enter the URL deliveries are sent to.";
  if (!/^https:\/\//i.test(url)) return "The URL must start with https://.";
  if (draft.eventTypes.length === 0) return "Choose at least one event type.";
  return null;
}

export function createInput(draft: EndpointDraft) {
  return {
    url: draft.url.trim(),
    description: draft.description.trim() || null,
    eventTypes: [...draft.eventTypes],
  };
}

function sameTypes(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((item) => set.has(item));
}

/**
 * UpdateWebhookEndpointInput with only the changed fields, or null when
 * nothing changed. Omitted fields keep their value on the server.
 */
export function updateInput(
  original: WebhookEndpointRow,
  draft: EndpointDraft
): { url?: string; description?: string; eventTypes?: string[] } | null {
  const input: { url?: string; description?: string; eventTypes?: string[] } = {};
  const url = draft.url.trim();
  const description = draft.description.trim();
  if (url !== original.url) input.url = url;
  if (description !== original.description) input.description = description;
  if (!sameTypes(draft.eventTypes, original.eventTypes)) input.eventTypes = [...draft.eventTypes];
  return Object.keys(input).length > 0 ? input : null;
}

export type MutationOutcome<T> =
  | { ok: true; value: T }
  | {
      ok: false;
      kind: "validation" | "not_found" | "rate_limit" | "auth" | "error";
      message: string;
    };

const errorKinds: Record<string, "validation" | "not_found" | "rate_limit" | "auth"> = {
  ValidationError: "validation",
  NotFoundError: "not_found",
  RateLimitError: "rate_limit",
  AuthError: "auth",
};

/**
 * Classifies a webhook mutation union result. `successTypename` is the
 * member that carries the result; every error member keeps its message, and
 * transport or GraphQL errors fall back to the first error message.
 */
export function mutationOutcome<T extends object>(
  result: T | null | undefined,
  errors: readonly { message: string }[] | null | undefined,
  successTypename: string,
  fallback: string
): MutationOutcome<T> {
  const typename =
    result && "__typename" in result && typeof result.__typename === "string"
      ? result.__typename
      : "";
  if (result && typename === successTypename) return { ok: true, value: result };
  if (result && typename in errorKinds) {
    const message =
      "message" in result && typeof result.message === "string" && result.message
        ? result.message
        : fallback;
    return { ok: false, kind: errorKinds[typename], message };
  }
  return { ok: false, kind: "error", message: errors?.[0]?.message || fallback };
}

/** A signing secret shown once, after the endpoint was created or its secret rotated. */
export interface RevealedSecret {
  endpointId: string;
  url: string;
  secret: string;
  reason: "created" | "rotated";
}

/**
 * Extracts the one-time secret from a create or rotate result. Only the
 * WebhookEndpointSecret member carries one; no query returns it.
 */
export function revealedSecret(
  result: object | null | undefined,
  reason: RevealedSecret["reason"]
): RevealedSecret | null {
  if (!result || !("__typename" in result) || result.__typename !== "WebhookEndpointSecret") {
    return null;
  }
  if (!("secret" in result) || typeof result.secret !== "string" || !result.secret) return null;
  if (!("endpoint" in result) || !result.endpoint || typeof result.endpoint !== "object") {
    return null;
  }
  const endpoint = result.endpoint as { id?: unknown; url?: unknown };
  if (typeof endpoint.id !== "string") return null;
  return {
    endpointId: endpoint.id,
    url: typeof endpoint.url === "string" ? endpoint.url : "",
    secret: result.secret,
    reason,
  };
}
