import type { TenantEvents$result } from "$houdini";

// Public tenant event types of clips, recordings, and uploads. They carry only
// public identifiers and sizes: no progress, storage paths, or node IDs.
export const CLIP_EVENT_TYPES = ["clip.requested", "clip.ready", "clip.failed"];
export const RECORDING_EVENT_TYPES = ["recording.ready", "recording.failed"];
export const UPLOAD_EVENT_TYPES = [
  "upload.created",
  "upload.completed",
  "upload.aborted",
  "upload.ready",
  "upload.failed",
];
export const ARTIFACT_EVENT_TYPES = [
  ...CLIP_EVENT_TYPES,
  ...RECORDING_EVENT_TYPES,
  ...UPLOAD_EVENT_TYPES,
];

type TenantEvent = NonNullable<TenantEvents$result["tenantEvents"]>;

export type ArtifactEventKind = "clips" | "dvr" | "vod";

export interface ArtifactEvent {
  id: string;
  type: string;
  // The part of the type after the family, e.g. "ready" for clip.ready.
  stage: string;
  // True for events after which the artifact does not change stage again.
  terminal: boolean;
  time: string;
  artifactId: string;
  kind: ArtifactEventKind;
  streamId: string | null;
  playbackId: string | null;
  durationMs: number | null;
  sizeBytes: number | null;
  reason: string | null;
}

const TERMINAL_STAGES = new Set(["ready", "failed", "aborted"]);

function kindOf(family: string): ArtifactEventKind | null {
  if (family === "clip") return "clips";
  if (family === "recording") return "dvr";
  if (family === "upload") return "vod";
  return null;
}

function numberOrNull(value: unknown): number | null {
  return typeof value === "number" ? value : null;
}

// artifactEventFromTenantEvent flattens a clip, recording, or upload event from
// the tenantEvents subscription; any other event returns null.
export function artifactEventFromTenantEvent(event: TenantEvent): ArtifactEvent | null {
  const [family, stage] = event.type.split(".", 2);
  const kind = kindOf(family);
  const data = event.data as Record<string, unknown> & {
    artifact?: {
      artifactId: string;
      streamId: string;
      playbackId: string;
    } | null;
  };
  const artifact = data?.artifact;
  if (!kind || !stage || !artifact?.artifactId) return null;
  return {
    id: event.id,
    type: event.type,
    stage,
    terminal: TERMINAL_STAGES.has(stage),
    time: String(event.time),
    artifactId: artifact.artifactId,
    kind,
    streamId: artifact.streamId || null,
    playbackId: artifact.playbackId || null,
    durationMs: numberOrNull(data.durationMs),
    sizeBytes: numberOrNull(data.sizeBytes),
    reason: typeof data.reason === "string" ? data.reason : null,
  };
}

// upsertArtifactEvent keeps the latest event per artifact, newest first.
export function upsertArtifactEvent(
  events: ArtifactEvent[],
  event: ArtifactEvent,
  limit = 100
): ArtifactEvent[] {
  return [event, ...events.filter((e) => e.artifactId !== event.artifactId)].slice(0, limit);
}
