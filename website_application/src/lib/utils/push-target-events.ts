export type PushTargetRefreshEvent = {
  type: string;
  payload?: unknown;
};

export function shouldRefreshPushTargets(event: PushTargetRefreshEvent): boolean {
  const eventType = event.type.toUpperCase();
  if (eventType.includes("PUSH") || eventType.includes("RESTREAM")) return true;
  if (eventType !== "STREAM_LIFECYCLE_UPDATE") return false;

  let payload = event.payload;
  if (typeof payload === "string") {
    try {
      payload = JSON.parse(payload);
    } catch {
      return false;
    }
  }
  if (!payload || typeof payload !== "object") return false;
  const data = payload as Record<string, unknown>;
  const rawFields = data.changedFields ?? data.changed_fields;
  if (!Array.isArray(rawFields)) return false;
  return rawFields.some((field) => field === "push_targets" || field === "push_target_status");
}
