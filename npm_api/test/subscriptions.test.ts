import type { AddressInfo } from "node:net";

import { describe, expect, it } from "vitest";
import { WebSocketServer } from "ws";

import { TenantEventsDocument } from "../src/generated/graphql.js";
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
    tokens: Array<string | null>;
    connections: ConnectionScript[];
    expect: {
      connections: number;
      authorization: Array<string | null>;
      eventIds: string[];
      error?: ExpectedError;
    };
  }>;
}

const fixture = loadFixture<SubscriptionsFixture>("subscriptions.json");

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
      const eventIds: string[] = [];
      let error: unknown = null;
      try {
        for await (const data of client.subscribe(TenantEventsDocument, {})) {
          eventIds.push(data.tenantEvents.id);
        }
      } catch (err) {
        error = err;
      } finally {
        await client.close();
        await new Promise((resolve) => wss.close(resolve));
      }
      expect(eventIds).toEqual(tc.expect.eventIds);
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
