import { createServer } from "node:http";
import type { AddressInfo } from "node:net";

import { Webhook } from "standardwebhooks";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  createWebhookReceiver,
  parseWebhookEvent,
  WebhookVerificationError,
  type WebhookEvent,
} from "../src/webhooks.js";
import { loadFixture } from "./fixtures.js";

interface WebhooksFixture {
  verify: Array<{
    name: string;
    secret: string;
    headers: Record<string, string>;
    body: string;
    now: number;
    valid: boolean;
  }>;
  parse: Array<{
    name: string;
    body: string;
    expect: {
      known: boolean;
      id: string;
      type: string;
      apiVersion: string;
      createdAt: string;
      expectFields?: Record<string, string>;
      rawData?: unknown;
    };
  }>;
}

const fixture = loadFixture<WebhooksFixture>("webhooks.json");

/** Reads a dot path from a decoded payload in its protobuf JSON form: 64-bit integers as strings. */
function protoJSONPath(data: unknown, path: string): unknown {
  let value: unknown = data;
  for (const part of path.split(".")) {
    value = (value as Record<string, unknown> | undefined)?.[part];
  }
  return typeof value === "bigint" ? value.toString() : value;
}

afterEach(() => {
  vi.useRealTimers();
});

describe("webhook verification (sdk_conformance/webhooks.json)", () => {
  for (const tc of fixture.verify) {
    it(tc.name, () => {
      vi.useFakeTimers();
      vi.setSystemTime(tc.now * 1000);
      const receiver = createWebhookReceiver(tc.secret);
      const verify = () => receiver.verify(tc.body, tc.headers);
      if (tc.valid) {
        expect(verify).not.toThrow();
      } else {
        expect(verify).toThrow(WebhookVerificationError);
      }
    });
  }
});

describe("webhook parsing (sdk_conformance/webhooks.json)", () => {
  for (const tc of fixture.parse) {
    it(tc.name, () => {
      const event = parseWebhookEvent(tc.body);
      expect(event.known).toBe(tc.expect.known);
      expect(event.id).toBe(tc.expect.id);
      expect(event.type).toBe(tc.expect.type);
      expect(event.apiVersion).toBe(tc.expect.apiVersion);
      expect(event.createdAt).toBe(tc.expect.createdAt);
      if (tc.expect.known) {
        for (const [path, want] of Object.entries(tc.expect.expectFields ?? {})) {
          expect(protoJSONPath(event.data, path), path).toBe(want);
        }
      } else {
        expect(event.data).toEqual(tc.expect.rawData);
      }
    });
  }

  it("narrows data by event type", () => {
    const event: WebhookEvent = parseWebhookEvent(fixture.parse[0]!.body);
    if (event.known && event.type === "clip.ready") {
      expect(event.data.sizeBytes).toBe(7340032n);
    } else {
      throw new Error("clip.ready did not parse as a known event");
    }
  });
});

describe("webhook round trip against a local server", () => {
  it("receives a request signed by the Standard Webhooks signer", async () => {
    const secret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw";
    const receiver = createWebhookReceiver(secret);
    const received: WebhookEvent[] = [];
    const server = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        try {
          received.push(receiver.receive(Buffer.concat(chunks), req.headers));
          res.writeHead(204).end();
        } catch {
          res.writeHead(401).end();
        }
      });
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/`;
      const body = fixture.parse[1]!.body;
      const signer = new Webhook(secret);
      const msgId = "0199a3c3-0000-7000-8000-000000000001";
      const now = new Date();
      const send = (signature: string) =>
        fetch(url, {
          method: "POST",
          headers: {
            "content-type": "application/json",
            "webhook-id": msgId,
            "webhook-timestamp": String(Math.floor(now.getTime() / 1000)),
            "webhook-signature": signature,
          },
          body,
        });
      expect((await send(signer.sign(msgId, now, body))).status).toBe(204);
      expect(
        (await send(new Webhook("whsec_" + btoa("another secret key!!")).sign(msgId, now, body)))
          .status
      ).toBe(401);
      expect(received).toHaveLength(1);
      expect(received[0]!.type).toBe("stream.live");
    } finally {
      await new Promise((resolve) => server.close(resolve));
    }
  });
});
