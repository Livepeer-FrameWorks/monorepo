import type { StreamMetricsListFields$data } from "$houdini";

export type StreamMetricsDisplay = Pick<
  StreamMetricsListFields$data,
  "currentViewers" | "qualityTier" | "primaryWidth" | "primaryHeight"
>;

export function streamCurrentViewers(metrics: StreamMetricsDisplay | null | undefined): number {
  return metrics?.currentViewers ?? 0;
}

export function streamResolutionLabel(metrics: StreamMetricsDisplay | null | undefined): string {
  const width = metrics?.primaryWidth;
  const height = metrics?.primaryHeight;
  if (width && height) return `${width}x${height}`;
  return metrics?.qualityTier || "Unknown";
}
