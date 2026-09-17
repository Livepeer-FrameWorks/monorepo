import type { IncidentFields$data, IncidentUpdates$result } from "$houdini";

export type IncidentStatus = "FIRING" | "ACKNOWLEDGED" | "RESOLVED";
export type IncidentRow = IncidentFields$data;
export type IncidentUpdate = NonNullable<IncidentUpdates$result["liveIncidentUpdates"]>;

export const incidentStatuses: { value: IncidentStatus; label: string }[] = [
  { value: "FIRING", label: "Firing" },
  { value: "ACKNOWLEDGED", label: "Acknowledged" },
  { value: "RESOLVED", label: "Resolved" },
];

export function incidentStatusLabel(status: string): string {
  return incidentStatuses.find((item) => item.value === status)?.label ?? status;
}

/** Tailwind classes for a status badge; firing is the only state that needs attention. */
export function incidentStatusClass(status: string): string {
  switch (status) {
    case "FIRING":
      return "border-destructive/60 text-destructive";
    case "ACKNOWLEDGED":
      return "border-warning/60 text-warning";
    default:
      return "border-success/60 text-success";
  }
}

export function incidentSeverityClass(severity: string): string {
  switch (severity.toLowerCase()) {
    case "critical":
      return "border-destructive/60 text-destructive";
    case "warning":
      return "border-warning/60 text-warning";
    default:
      return "border-border text-muted-foreground";
  }
}

export interface IncidentTimelineEntry {
  id: string;
  kind: string;
  actorUserId: string | null;
  createdAt: string;
  note: string | null;
  assignedTo: string | null;
  reportId: string | null;
  channel: string | null;
  resolution: string | null;
  alertFingerprint: string | null;
  alertname: string | null;
}

const channelLabels: Record<string, string> = {
  email: "email",
  slack: "Slack",
  discord: "Discord",
};

/** One-line description of a timeline event. NOTE bodies and report links render separately. */
export function describeTimelineEvent(event: IncidentTimelineEntry): string {
  const alert = event.alertname ?? "An alert";
  switch (event.kind) {
    case "ALERT_FIRING":
      return `${alert} started firing`;
    case "ALERT_RESOLVED":
      return `${alert} resolved`;
    case "ACKNOWLEDGED":
      return "Acknowledged";
    case "ASSIGNED":
      return event.assignedTo ? `Assigned to ${event.assignedTo}` : "Assignment cleared";
    case "NOTE":
      return "Note added";
    case "RESOLVED":
      return event.resolution === "AUTO"
        ? "Resolved after every alert cleared"
        : "Resolved manually";
    case "INVESTIGATION_ATTACHED":
      return "Skipper investigation attached";
    case "NOTIFIED":
      return event.channel
        ? `Operators notified by ${channelLabels[event.channel] ?? event.channel}`
        : "Operators notified";
    case "SCOPE_CHANGED":
      return "Visibility changed after the cluster owner was confirmed";
    default:
      return event.kind;
  }
}

export function formatIncidentTime(value: string | null | undefined): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Renders alert label/annotation maps as sorted key/value pairs. */
export function labelEntries(
  value: Record<string, unknown> | null | undefined
): [string, string][] {
  if (!value) return [];
  return Object.entries(value)
    .map(([key, item]): [string, string] => [
      key,
      typeof item === "string" ? item : JSON.stringify(item),
    ])
    .sort(([a], [b]) => a.localeCompare(b));
}

/** Null when an incident mutation succeeded; otherwise the message to show. */
export function mutationErrorMessage(
  result: object | null | undefined,
  errors: readonly { message: string }[] | null | undefined,
  fallback: string
): string | null {
  if (result && "__typename" in result && result.__typename === "Incident") return null;
  if (result && "message" in result && typeof result.message === "string" && result.message) {
    return result.message;
  }
  if (errors?.length) return errors[0].message;
  return fallback;
}

export interface IncidentListFilter {
  statuses: readonly string[];
  clusterId: string;
}

export interface IncidentListUpdate {
  rows: IncidentRow[];
  /** The loaded rows cannot be brought up to date from the events alone. */
  refetch: boolean;
}

/**
 * Changes that only alter fields the event carries (status, severity, title,
 * updated time) among those a list row shows. Alert changes also move the
 * firing count and last alert time, and `opened`/`scope_changed` add or remove
 * rows, so those need the list again.
 */
const inPlaceIncidentChanges = new Set([
  "acknowledged",
  "resolved",
  "assigned",
  "note",
  "investigation_attached",
]);

function matchesIncidentFilter(event: IncidentUpdate, filter: IncidentListFilter): boolean {
  if (filter.statuses.length > 0 && !filter.statuses.includes(event.status)) return false;
  return !filter.clusterId || event.clusterId === filter.clusterId;
}

/**
 * Applies realtime incident changes to the loaded rows of a filtered list.
 * Lists are ordered by creation time, which no change alters, so updated rows
 * keep their position.
 */
export function applyIncidentUpdates(
  rows: readonly IncidentRow[],
  events: readonly IncidentUpdate[],
  filter: IncidentListFilter,
  hasNextPage: boolean
): IncidentListUpdate {
  const next = [...rows];
  let refetch = false;
  for (const event of events) {
    const index = next.findIndex((row) => row.id === event.incidentId);
    if (index === -1) {
      // An unloaded incident can join this list, or can leave a status-filtered
      // list from a page that is not loaded yet, which changes the total.
      if (
        event.change === "opened" ||
        event.change === "scope_changed" ||
        matchesIncidentFilter(event, filter) ||
        (hasNextPage && filter.statuses.length > 0)
      ) {
        refetch = true;
      }
      continue;
    }
    const row = next[index];
    const eventTime = Date.parse(event.updatedAt);
    const rowTime = Date.parse(row.updatedAt);
    if (!Number.isNaN(eventTime) && !Number.isNaN(rowTime) && eventTime < rowTime) continue;
    if (!inPlaceIncidentChanges.has(event.change) || !matchesIncidentFilter(event, filter)) {
      refetch = true;
      continue;
    }
    next[index] = {
      ...row,
      status: event.status,
      severity: event.severity,
      title: event.title,
      updatedAt: event.updatedAt,
    };
  }
  return { rows: next, refetch };
}
