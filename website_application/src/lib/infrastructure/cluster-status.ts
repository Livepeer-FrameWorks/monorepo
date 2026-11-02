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

export function isUnknownClusterStatus(healthStatus: string | null | undefined): boolean {
  const status = (healthStatus ?? "").trim().toLowerCase();
  return status === "" || status === "unknown";
}
