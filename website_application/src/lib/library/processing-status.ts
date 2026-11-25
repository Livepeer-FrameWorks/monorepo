const waitLabels: Record<string, string> = {
  memory: "memory",
  slots: "processing slots",
  fitting_node_unavailable: "suitable node unavailable",
  cluster_unavailable: "cluster unavailable",
  dispatch_retry: "retrying dispatch",
};

export function processingStatusLabel(
  status: string,
  expired: boolean,
  waitReason?: string | null
): string {
  const state = status.toLowerCase();
  if (expired || state === "expired") return "Expired";
  if (waitReason && ["queued", "processing", "requested", "starting"].includes(state)) {
    const reason = waitLabels[waitReason];
    return reason ? `Queued (${reason})` : "Queued";
  }
  return status || "Unknown";
}
