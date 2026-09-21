/**
 * Webhook verification and typed parsing. verify checks the Standard
 * Webhooks signature with the official standardwebhooks library; parse
 * decodes the body into the generated payload type of its event type. An
 * event type this SDK version does not know is returned as a raw event, so a
 * new event type never breaks an older SDK.
 */
import {
  Webhook,
  WebhookVerificationError as StandardWebhookVerificationError,
} from "standardwebhooks";

import { FrameWorksError } from "./errors.js";
import {
  publicEventDecoders,
  type PublicEventDataMap,
  type PublicEventType,
} from "./generated/events.js";

export type { PublicEventDataMap, PublicEventType } from "./generated/events.js";
export * from "./generated/proto/events/public/v1/common.js";
export * from "./generated/proto/events/public/v1/access.js";
export * from "./generated/proto/events/public/v1/billing.js";
export * from "./generated/proto/events/public/v1/media.js";
export * from "./generated/proto/events/public/v1/streams.js";

/** The signature, timestamp, or headers of a webhook request did not verify. */
export class WebhookVerificationError extends FrameWorksError {
  constructor(message: string, cause?: unknown) {
    super(message, { cause });
    this.name = "WebhookVerificationError";
  }
}

interface EventEnvelope {
  /** Event ID, equal to the webhook-id header; deduplicate on it. */
  id: string;
  /** The payload version the endpoint pins, "v1". */
  apiVersion: string;
  /** When the change committed, RFC 3339. */
  createdAt: string;
}

/** An event of a type this SDK knows, with its payload decoded. */
export type KnownWebhookEvent = {
  [K in PublicEventType]: EventEnvelope & { known: true; type: K; data: PublicEventDataMap[K] };
}[PublicEventType];

/** An event of a type this SDK does not know; data is the JSON payload as received. */
export interface RawWebhookEvent extends EventEnvelope {
  known: false;
  type: string;
  data: unknown;
}

export type WebhookEvent = KnownWebhookEvent | RawWebhookEvent;

/** Request headers as fetch Headers or a Node IncomingHttpHeaders-like record. */
export type WebhookHeaders =
  | { get(name: string): string | null }
  | Readonly<Record<string, string | ReadonlyArray<string> | undefined>>;

const signatureHeaders = ["webhook-id", "webhook-timestamp", "webhook-signature"] as const;

function headerRecord(headers: WebhookHeaders): Record<string, string> {
  const out: Record<string, string> = {};
  if (typeof (headers as { get?: unknown }).get === "function") {
    const h = headers as { get(name: string): string | null };
    for (const name of signatureHeaders) {
      const v = h.get(name);
      if (v !== null) {
        out[name] = v;
      }
    }
    return out;
  }
  const record = headers as Readonly<Record<string, string | ReadonlyArray<string> | undefined>>;
  for (const [key, value] of Object.entries(record)) {
    const name = key.toLowerCase();
    if ((signatureHeaders as ReadonlyArray<string>).includes(name) && value !== undefined) {
      out[name] = typeof value === "string" ? value : value.join(" ");
    }
  }
  return out;
}

function payloadText(payload: string | Uint8Array): string {
  return typeof payload === "string" ? payload : new TextDecoder().decode(payload);
}

/** Parses a webhook body into a typed event, without verifying it. */
export function parseWebhookEvent(payload: string | Uint8Array): WebhookEvent {
  let body: unknown;
  try {
    body = JSON.parse(payloadText(payload));
  } catch (err) {
    throw new FrameWorksError("webhook body is not JSON", { cause: err });
  }
  if (body === null || typeof body !== "object") {
    throw new FrameWorksError("webhook body is not an event object");
  }
  const obj = body as Record<string, unknown>;
  const envelope = {
    id: String(obj.id ?? ""),
    apiVersion: String(obj.api_version ?? ""),
    createdAt: String(obj.created_at ?? ""),
  };
  const type = String(obj.type ?? "");
  const decoder = (
    publicEventDecoders as Record<string, { fromJSON(o: unknown): unknown } | undefined>
  )[type];
  if (!decoder) {
    return { ...envelope, known: false, type, data: obj.data };
  }
  return {
    ...envelope,
    known: true,
    type,
    data: decoder.fromJSON(obj.data ?? {}),
  } as KnownWebhookEvent;
}

export interface WebhookReceiver {
  /** Throws WebhookVerificationError unless the headers sign payload with this secret within five minutes. */
  verify(payload: string | Uint8Array, headers: WebhookHeaders): void;
  /** verify, then parseWebhookEvent. */
  receive(payload: string | Uint8Array, headers: WebhookHeaders): WebhookEvent;
}

/**
 * A receiver for one endpoint secret (whsec_...). Pass the raw request body:
 * parsing and re-serializing JSON changes the bytes that were signed.
 */
export function createWebhookReceiver(secret: string): WebhookReceiver {
  const wh = new Webhook(secret);
  const verify = (payload: string | Uint8Array, headers: WebhookHeaders) => {
    try {
      wh.verify(payloadText(payload), headerRecord(headers), { jsonParse: false });
    } catch (err) {
      if (err instanceof StandardWebhookVerificationError) {
        throw new WebhookVerificationError(err.message, err);
      }
      throw err;
    }
  };
  return {
    verify,
    receive(payload, headers) {
      verify(payload, headers);
      return parseWebhookEvent(payload);
    },
  };
}
