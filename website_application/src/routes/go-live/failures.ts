/**
 * What the Go Live page tells a broadcaster when the destination lookup or
 * the studio fails, keyed on the gateway error code or the studio's error
 * text. Every branch names the next step; none repeats raw error text, which
 * can carry the stream key inside a WHIP URL.
 */
export type GoLiveFailureAction = "keys" | "billing" | "login" | "settings" | "retry";

export interface GoLiveFailure {
  title: string;
  detail: string;
  actions: GoLiveFailureAction[];
}

const ACCESS_CODES = new Set([
  "FORBIDDEN",
  "NOT_FOUND",
  "STREAM_MISMATCH",
  "NO_STREAM_KEY",
  "VALIDATION_ERROR",
]);
const BALANCE_CODES = new Set(["PAYMENT_REQUIRED", "INSUFFICIENT_BALANCE"]);
const CAPACITY_CODES = new Set([
  "UNAVAILABLE",
  "NO_DESTINATION",
  "PROTOCOL_UNAVAILABLE",
  "RATE_LIMITED",
  "TIMEOUT",
  "NETWORK",
]);

const balanceFailure: GoLiveFailure = {
  title: "Your prepaid balance is spent",
  detail:
    "New broadcasts are refused at €0. Top up your balance, then go live again. Streams already running keep going.",
  actions: ["billing"],
};

const refusedFailure: GoLiveFailure = {
  title: "The ingest node refused this broadcast",
  detail:
    "This happens when the stream key is no longer active or a prepaid balance is spent. Check the stream's keys and your balance, then go live again.",
  actions: ["keys", "billing"],
};

const unreachableFailure: GoLiveFailure = {
  title: "No ingest node could take this broadcast",
  detail:
    "Check your internet connection and try again. Each attempt looks up the best node again.",
  actions: ["retry"],
};

export function describeResolveFailure(code: string | null | undefined): GoLiveFailure {
  if (code === "UNAUTHORIZED") {
    return {
      title: "Your session has ended",
      detail: "Sign in again to broadcast.",
      actions: ["login"],
    };
  }
  if (code && ACCESS_CODES.has(code)) {
    return {
      title: "This stream's key was not accepted",
      detail: "Open the stream, check that it has an active key, then try again.",
      actions: ["keys", "retry"],
    };
  }
  if (code && (BALANCE_CODES.has(code) || code.includes("BALANCE"))) return balanceFailure;
  if (code && CAPACITY_CODES.has(code)) return unreachableFailure;
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
  if (/WHIP request failed: 402/.test(message)) return balanceFailure;
  if (/WHIP request failed: 40[13]/.test(message)) return refusedFailure;
  if (
    /WHIP request failed: 5\d\d|Connection (failed|timeout)|reconnection attempts exhausted|resolution/i.test(
      message
    )
  ) {
    return unreachableFailure;
  }
  return {
    title: "The broadcast stopped",
    detail:
      "Click Go Live to try again; your sources stay in place. The Go Live guide lists fixes for common problems.",
    actions: [],
  };
}
