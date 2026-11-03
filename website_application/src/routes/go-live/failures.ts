/**
 * What the Go Live page tells a broadcaster when the destination lookup or
 * the studio fails, keyed on the gateway's denial reason, its error code or
 * the studio's error text. Every branch names the next step; none repeats raw
 * error text, which can carry the stream key inside a WHIP URL.
 */
import {
  BALANCE_REFUSAL,
  CONFLICT_REFUSAL,
  KEY_REFUSAL,
  UNREACHABLE_REFUSAL,
  describeIngestRefusal,
  type IngestRefusal,
  type IngestRefusalAction,
} from "$lib/placement/ingest-refusals";

export type GoLiveFailureAction = IngestRefusalAction;
export type GoLiveFailure = IngestRefusal;

const KEY_CODES = new Set(["NOT_FOUND", "STREAM_MISMATCH", "NO_STREAM_KEY", "VALIDATION_ERROR"]);
// Foghorn refuses a negative prepaid balance with gRPC FailedPrecondition,
// presented as FAILED_PRECONDITION. The page lists only push streams, so the
// non-push FailedPrecondition denial cannot reach this branch.
const BALANCE_CODES = new Set(["FAILED_PRECONDITION", "PAYMENT_REQUIRED"]);
const CAPACITY_CODES = new Set([
  "UNAVAILABLE",
  "NO_DESTINATION",
  "PROTOCOL_UNAVAILABLE",
  "RATE_LIMITED",
  "TIMEOUT",
  "NETWORK",
]);

const refusedFailure: GoLiveFailure = {
  title: "The ingest node refused this broadcast",
  detail:
    "This happens when the stream key is no longer active or a prepaid balance is spent. Check the stream's keys and your balance, then go live again.",
  actions: ["keys", "billing"],
};

// Go Live publishes over WHIP only, so a protocol refusal names the browser.
const whipRefusedFailure: GoLiveFailure = {
  title: "This stream's cluster does not take browser broadcasts",
  detail:
    "The cluster refused WHIP ingest. Broadcast with OBS over RTMP or SRT, or move the stream to another cluster.",
  actions: ["keys"],
};

export function describeResolveFailure(
  code: string | null | undefined,
  reason?: string | null
): GoLiveFailure {
  if (reason === "PROTOCOL_NOT_SUPPORTED") return whipRefusedFailure;
  const refusal = describeIngestRefusal(reason);
  if (refusal) return refusal;
  if (code === "UNAUTHORIZED") {
    return {
      title: "Your session has ended",
      detail: "Sign in again to broadcast.",
      actions: ["login"],
    };
  }
  if (code === "FORBIDDEN") {
    return {
      title: "This stream or account cannot broadcast right now",
      detail:
        "The stream's key may be inactive, or the account may be suspended or not allowed on this cluster. Check the stream's keys and your billing, then try again.",
      actions: ["keys", "billing"],
    };
  }
  if (code === "CONFLICT") return CONFLICT_REFUSAL;
  if (code && KEY_CODES.has(code)) return KEY_REFUSAL;
  if (code && BALANCE_CODES.has(code)) return BALANCE_REFUSAL;
  if (code && CAPACITY_CODES.has(code)) return UNREACHABLE_REFUSAL;
  return {
    title: "Go Live could not find an ingest node",
    detail: "Try again in a moment. If it keeps failing, contact support from the Messages page.",
    actions: ["retry"],
  };
}

export function describeStudioFailure(message: string): GoLiveFailure {
  if (/NotAllowedError|PermissionDenied|Permission (request )?(failed|denied)/i.test(message)) {
    return {
      title: "The browser blocked your camera or microphone",
      detail:
        "Allow camera and microphone access for this site in your browser's site settings, then add the source again. Your other sources and scenes stay in place.",
      actions: [],
    };
  }
  if (/NotFoundError|OverconstrainedError|Requested device not found/i.test(message)) {
    return {
      title: "No camera or microphone was found",
      detail: "Connect a device or pick another one, then add the source again.",
      actions: [],
    };
  }
  if (/NotReadableError|Could not start (video|audio) source|Device in use/i.test(message)) {
    return {
      title: "Your camera or microphone is in use",
      detail: "Close other apps or tabs that use the device, then add the source again.",
      actions: [],
    };
  }
  if (/No media source available/i.test(message)) {
    return {
      title: "Add a source first",
      detail: "Add a camera, a screen share or a media file before you go live.",
      actions: [],
    };
  }
  if (/Compositor initialization failed|compositor/i.test(message)) {
    return {
      title: "The compositor could not start in this browser",
      detail:
        "Turn off the compositor in Studio settings to broadcast one source at a time, or switch to a recent Chrome or Edge.",
      actions: ["settings"],
    };
  }
  // The WHIP error carries the response body, which names an ended operator grant.
  if (/WHIP request failed: 402/.test(message) && /GRANT_EXPIRED/.test(message)) {
    return describeIngestRefusal("GRANT_EXPIRED");
  }
  if (/WHIP request failed: 402/.test(message)) return BALANCE_REFUSAL;
  if (/WHIP request failed: 40[13]/.test(message)) return refusedFailure;
  if (
    /WHIP request failed: 5\d\d|Connection (failed|timeout)|reconnection attempts exhausted|resolution/i.test(
      message
    )
  ) {
    return UNREACHABLE_REFUSAL;
  }
  return {
    title: "The broadcast stopped",
    detail:
      "Click Go Live to try again; your sources stay in place. The Go Live guide lists fixes for common problems.",
    actions: [],
  };
}
