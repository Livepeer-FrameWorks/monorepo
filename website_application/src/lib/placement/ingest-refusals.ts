/**
 * What a refused ingest resolution tells the broadcaster, keyed on Foghorn's
 * ingest denial code. The gateway presents that code as `extensions.reason`
 * (api_balancing/internal/control/ingest.go EvaluateIngestAdmission).
 * INGEST_DENIED says nothing more specific than `extensions.code`, so it has
 * no entry and callers fall back to the code.
 */
export type IngestRefusalAction = "keys" | "billing" | "login" | "settings" | "retry";

export interface IngestRefusal {
  title: string;
  detail: string;
  actions: IngestRefusalAction[];
}

export const BALANCE_REFUSAL: IngestRefusal = {
  title: "Your prepaid balance is spent",
  detail:
    "New broadcasts are refused at €0. Top up your balance, then go live again. Streams already running keep going, and stop if the balance falls below -€10.",
  actions: ["billing"],
};

export const UNREACHABLE_REFUSAL: IngestRefusal = {
  title: "No ingest node could take this broadcast",
  detail:
    "Check your internet connection and try again. Each attempt looks up the best node again.",
  actions: ["retry"],
};

export const CONFLICT_REFUSAL: IngestRefusal = {
  title: "This stream is already receiving video from another encoder",
  detail: "Stop the other encoder, then go live again.",
  actions: ["retry"],
};

export const KEY_REFUSAL: IngestRefusal = {
  title: "This stream's key was not accepted",
  detail: "Open the stream, check that it has an active key, then try again.",
  actions: ["keys", "retry"],
};

const REASON_REFUSALS: Record<string, IngestRefusal> = {
  PAYMENT_REQUIRED: BALANCE_REFUSAL,
  INVALID_STREAM_KEY: KEY_REFUSAL,
  DUPLICATE_INGEST: CONFLICT_REFUSAL,
  CLUSTER_UNHEALTHY: UNREACHABLE_REFUSAL,
  GRANT_EXPIRED: {
    title: "Your FrameWorks trial or arranged plan has ended",
    detail:
      "Broadcasting pauses while the account moves back to its standard plan, which takes a few minutes. Check that plan on the billing page, then go live again. To extend the arrangement, contact support from the Messages page.",
    actions: ["billing", "retry"],
  },
  ACCOUNT_INACTIVE: {
    title: "This account is deactivated",
    detail:
      "A deactivated account cannot broadcast. Contact support from the Messages page to reactivate it.",
    actions: [],
  },
  ACCOUNT_SUSPENDED: {
    title: "This account is suspended",
    detail:
      "Broadcasting is paused while the account is suspended, which follows an unpaid invoice or a spent prepaid balance. Settle it on the billing page, then go live again.",
    actions: ["billing"],
  },
  CLUSTER_NOT_ENTITLED: {
    title: "Your plan does not include this stream's cluster",
    detail:
      "Subscribe to the cluster or move the stream to a cluster your plan includes, then go live again.",
    actions: ["billing"],
  },
  CLUSTER_CLASS_MISMATCH: {
    title: "Your plan does not allow this kind of cluster",
    detail:
      "Change your plan or move the stream to a cluster your plan allows, then go live again.",
    actions: ["billing"],
  },
  PROTOCOL_NOT_SUPPORTED: {
    title: "This stream's cluster does not take this protocol",
    detail:
      "The cluster refused the requested ingest protocol. Pick another protocol, or move the stream to another cluster.",
    actions: ["keys"],
  },
  NON_PUSH_MODE_STREAM: {
    title: "This stream pulls its video from a source",
    detail:
      "Streams that pull from a source do not take a broadcast. Pick a stream that receives pushed video.",
    actions: ["keys"],
  },
};

/** The refusal for a denial reason, or null when the reason has no specific guidance. */
export function describeIngestRefusal(reason: string | null | undefined): IngestRefusal | null {
  if (!reason || !Object.hasOwn(REASON_REFUSALS, reason)) return null;
  return REASON_REFUSALS[reason];
}
