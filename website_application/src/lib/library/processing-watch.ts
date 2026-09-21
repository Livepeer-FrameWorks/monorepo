// The upload dialog watches a completed upload until vodUploadStatus reports a final state.
// No platform deadline ends VOD processing (the processing dispatcher only bounds a job's
// dispatch), so the dialog stops watching after a fixed limit and leaves the rest to the
// library, which shows the asset's state.
export const PROCESSING_WATCH_LIMIT_MS = 30 * 60_000;

export type ProcessingStatusResponse =
  | { __typename: string; state?: string | null; message?: string | null }
  | null
  | undefined;

export type ProcessingWatchVerdict =
  | { kind: "continue" }
  | { kind: "ready" }
  | { kind: "failed"; message: string }
  | { kind: "stopped"; message: string };

// A null response is a fetch that failed in transit; it keeps the watch going until the
// limit, since the next poll may succeed. NotFound, Auth, and Validation answers do not
// change on retry, so they end the watch at once.
export function processingWatchVerdict(
  response: ProcessingStatusResponse,
  startedAt: number,
  now: number
): ProcessingWatchVerdict {
  switch (response?.__typename) {
    case "VodUploadStatus":
      if (response.state === "READY") return { kind: "ready" };
      if (response.state === "FAILED") {
        return { kind: "failed", message: "The uploaded video could not be processed." };
      }
      if (response.state === "DELETED") {
        return {
          kind: "failed",
          message: "The uploaded video was deleted before it finished processing.",
        };
      }
      if (response.state === "EXPIRED") {
        return { kind: "failed", message: "The upload session expired before processing started." };
      }
      break;
    case "NotFoundError":
      return {
        kind: "stopped",
        message: "This upload no longer exists, so its processing status is unavailable.",
      };
    case "AuthError":
      return {
        kind: "stopped",
        message:
          "Your session can no longer read this upload's status. Sign in again and check the library.",
      };
    case "ValidationError":
      return {
        kind: "stopped",
        message: `The server rejected the upload status request${response.message ? `: ${response.message}` : "."}`,
      };
  }
  if (now - startedAt >= PROCESSING_WATCH_LIMIT_MS) {
    return {
      kind: "stopped",
      message:
        "Processing is taking longer than 30 minutes. It continues in the background; the library shows the video when it is ready.",
    };
  }
  return { kind: "continue" };
}
