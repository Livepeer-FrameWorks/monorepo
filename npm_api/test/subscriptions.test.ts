import type { AddressInfo } from "node:net";

import { describe, expect, expectTypeOf, it } from "vitest";
import { WebSocketServer } from "ws";

import * as documents from "../src/generated/graphql.js";
import {
  SkipperChatDocument,
  type SkipperChatSubscription,
  TenantEventsDocument,
  type TenantEventsSubscription,
} from "../src/generated/graphql.js";
import { createSubscriptionClient } from "../src/subscriptions.js";
import { errorMismatches, type ExpectedError, loadFixture } from "./fixtures.js";

interface ConnectionScript {
  onInit: "ack" | "close" | "reset";
  closeCode?: number;
  closeReason?: string;
  next?: unknown[];
  error?: unknown[];
  then?: "complete" | "drop";
  dropCode?: number;
}

interface SubscriptionsFixture {
  maxReconnects: number;
  cases: Array<{
    name: string;
    operation?: string;
    variables?: Record<string, unknown>;
    tokens: Array<string | null>;
    connections: ConnectionScript[];
    expect: {
      connections: number;
      authorization: Array<string | null>;
      eventIds?: string[];
      events?: unknown[];
      subscribed?: Array<{ operationName: string; variables: unknown }>;
      error?: ExpectedError;
    };
  }>;
}

const fixture = loadFixture<SubscriptionsFixture>("subscriptions.json");

function documentFor(name: string): string {
  const doc = (documents as Record<string, unknown>)[`${name}Document`];
  if (doc === undefined) {
    throw new Error(`no generated document ${name}Document`);
  }
  return String(doc);
}

/** Returns value with every null object member removed, at any depth. */
function withoutNulls(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(withoutNulls);
  }
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value)
        .filter(([, member]) => member !== null)
        .map(([key, member]) => [key, withoutNulls(member)])
    );
  }
  return value;
}

describe("subscription typing", () => {
  it("types each subscription's events and variables from its generated document", () => {
    const client = createSubscriptionClient({ url: "ws://127.0.0.1:1" });
    expectTypeOf(client.subscribe(TenantEventsDocument, {})).toEqualTypeOf<
      AsyncGenerator<TenantEventsSubscription, void, undefined>
    >();
    expectTypeOf(
      client.subscribe(SkipperChatDocument, { input: { message: "hello" } })
    ).toEqualTypeOf<AsyncGenerator<SkipperChatSubscription, void, undefined>>();
    // @ts-expect-error: SkipperChat's input variable is required.
    void client.subscribe(SkipperChatDocument, {});
  });
});

describe("subscriptions (sdk_conformance/subscriptions.json)", () => {
  for (const tc of fixture.cases) {
    it(tc.name, async () => {
      const authorization: Array<string | null> = [];
      let connections = 0;
      const wss = new WebSocketServer({
        port: 0,
        handleProtocols: (protocols) =>
          protocols.has("graphql-transport-ws") ? "graphql-transport-ws" : false,
      });
      const subscribed: Array<{ operationName: unknown; variables: unknown }> = [];
      wss.on("connection", (socket) => {
        const script = tc.connections[connections++];
        socket.on("message", (raw) => {
          const msg = JSON.parse(String(raw)) as {
            type: string;
            id?: string;
            payload?: Record<string, unknown>;
          };
          if (msg.type === "connection_init") {
            const auth = msg.payload?.Authorization;
            authorization.push(typeof auth === "string" ? auth : null);
            if (script?.onInit === "reset") {
              socket.terminate();
              return;
            }
            if (!script || script.onInit === "close") {
              socket.close(script?.closeCode ?? 1000, script?.closeReason ?? "terminated");
              return;
            }
            socket.send(JSON.stringify({ type: "connection_ack" }));
            return;
          }
          if (msg.type === "subscribe" && script) {
            subscribed.push({
              operationName: msg.payload?.operationName,
              variables: msg.payload?.variables,
            });
            for (const data of script.next ?? []) {
              socket.send(JSON.stringify({ id: msg.id, type: "next", payload: { data } }));
            }
            if (script.error) {
              socket.send(JSON.stringify({ id: msg.id, type: "error", payload: script.error }));
            } else if (script.then === "complete") {
              socket.send(JSON.stringify({ id: msg.id, type: "complete" }));
            } else if (script.then === "drop") {
              setTimeout(() => socket.close(script.dropCode ?? 1001, "going away"), 20);
            }
          }
        });
      });
      await new Promise((resolve) => wss.once("listening", resolve));
      const tokens = [...tc.tokens];
      const client = createSubscriptionClient({
        url: `ws://127.0.0.1:${(wss.address() as AddressInfo).port}`,
        token: () => tokens.shift() ?? null,
        maxReconnects: fixture.maxReconnects,
        retryWait: () => Promise.resolve(),
      });
      const operation = tc.operation ?? "TenantEvents";
      const events: unknown[] = [];
      let error: unknown = null;
      try {
        for await (const data of client.subscribe(documentFor(operation), tc.variables ?? {})) {
          events.push(data);
        }
      } catch (err) {
        error = err;
      } finally {
        await client.close();
        await new Promise((resolve) => wss.close(resolve));
      }
      if (tc.expect.eventIds) {
        const eventIds = (events as TenantEventsSubscription[]).map((e) => e.tenantEvents.id);
        expect(eventIds).toEqual(tc.expect.eventIds);
      }
      if (tc.expect.events) {
        expect(events).toEqual(tc.expect.events);
      }
      if (tc.expect.subscribed) {
        expect(subscribed.map(withoutNulls)).toEqual(tc.expect.subscribed);
      }
      expect(connections).toBe(tc.expect.connections);
      expect(authorization).toEqual(tc.expect.authorization);
      if (tc.expect.error) {
        expect(errorMismatches(error, tc.expect.error)).toEqual([]);
      } else {
        expect(error).toBeNull();
      }
    });
  }
});
