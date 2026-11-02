export const WAITING_FOR_FIRST_EDGE = "Waiting for first edge";

/**
 * The status shown for a cluster. A cluster's health stays "unknown" until an
 * edge reports in, so a cluster with that status and no enrolled nodes is
 * waiting for its first edge rather than in an unknown state. `nodeCount` is
 * null while the count is not loaded.
 */
export function clusterStatusLabel(
  healthStatus: string | null | undefined,
  nodeCount: number | null | undefined
): string {
  const status = (healthStatus ?? "").trim();
  if ((status === "" || status.toLowerCase() === "unknown") && nodeCount === 0) {
    return WAITING_FOR_FIRST_EDGE;
  }
  return status || "unknown";
}

/** Pill colours for a cluster health status; anything not reported as healthy stays neutral. */
export function clusterStatusClass(healthStatus: string | null | undefined): string {
  switch (healthStatus?.trim().toLowerCase()) {
    case "healthy":
      return "border-success/40 bg-success/10 text-success";
    case "degraded":
      return "border-warning/40 bg-warning/10 text-warning";
    case "unhealthy":
      return "border-rose-500/40 bg-rose-500/10 text-rose-300";
    default:
      return "border-muted-foreground/40 bg-muted-foreground/10 text-muted-foreground";
  }
}

export function isUnknownClusterStatus(healthStatus: string | null | undefined): boolean {
  const status = (healthStatus ?? "").trim().toLowerCase();
  return status === "" || status === "unknown";
}
