export type RecordingStateEvent = { type: string; timestamp: string; details?: string | null };
export type RecordingNotice = {
  state: "recording" | "blocked" | "pending" | "unknown";
  storage: boolean;
  at?: string;
  historical?: boolean;
};
export function recordingNotice(
  events: readonly RecordingStateEvent[],
  enabled: boolean,
  isLive: boolean,
  connectedAt?: string | null,
  unavailable = false
): RecordingNotice | null {
  if (!enabled) return null;
  const since = connectedAt ? Date.parse(connectedAt) : NaN;
  const ordered = [...events]
    .filter((e) => Number.isFinite(Date.parse(e.timestamp)))
    .sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp));
  for (const event of ordered) {
    if (isLive && Number.isFinite(since) && Date.parse(event.timestamp) < since) break;
    if (event.type === "STREAM_START" && isLive) break;
    if (event.type === "RECORDING_REFUSED")
      return {
        state: "blocked",
        storage: /storage limit/i.test(event.details ?? ""),
        at: event.timestamp,
        historical: !isLive,
      };
    if (event.type === "RECORDING_ACTIVE")
      return isLive
        ? { state: unavailable ? "unknown" : "recording", storage: false, at: event.timestamp }
        : null;
  }
  return isLive ? { state: unavailable ? "unknown" : "pending", storage: false } : null;
}
