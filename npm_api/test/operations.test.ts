import type { AddressInfo } from "node:net";

import { describe, expect, it } from "vitest";
import { WebSocketServer } from "ws";

import { createClientWith } from "../src/client.js";
import * as documents from "../src/generated/graphql.js";
import { operations } from "../src/generated/manifest.js";
import { createSubscriptionClient } from "../src/subscriptions.js";
import { loadFixture } from "./fixtures.js";

interface OperationsFixture {
  operations: Array<{
    name: string;
    kind: string;
    variables: Record<string, unknown>;
    response: { data: unknown };
  }>;
}

const fixture = loadFixture<OperationsFixture>("operations.json");

function documentFor(name: string): string {
  const doc = (documents as Record<string, unknown>)[`${name}Document`];
  if (doc === undefined) {
    throw new Error(`no generated document ${name}Document`);
  }
  return String(doc);
}

describe("generated operations (sdk_conformance/operations.json)", () => {
  it("covers every operation of the manifest", () => {
    expect(fixture.operations.map((o) => o.name).sort()).toEqual(Object.keys(operations).sort());
  });

  for (const op of fixture.operations.filter((o) => o.kind !== "subscription")) {
    it(`${op.name} sends its document and decodes a schema-shaped response`, async () => {
      const sent: Array<{ operationName: string; query: string; variables: unknown }> = [];
      const fetch = (async (_url: string, init?: RequestInit) => {
        sent.push(JSON.parse(String(init?.body)));
        return new Response(JSON.stringify(op.response), { status: 200 });
      }) as typeof globalThis.fetch;
      const client = createClientWith(
        { url: "https://operations.test/graphql", fetch, checkServer: false },
        {}
      );
      const data = await client.request(documentFor(op.name), op.variables);
      expect(data).toEqual(op.response.data);
      expect(sent).toHaveLength(1);
      expect(sent[0]!.operationName).toBe(op.name);
      expect(sent[0]!.query).toBe(documentFor(op.name));
      expect(sent[0]!.variables).toEqual(op.variables);
    });
  }

  for (const op of fixture.operations.filter((o) => o.kind === "subscription")) {
    it(`${op.name} subscribes with its document and yields a schema-shaped event`, async () => {
      const wss = new WebSocketServer({ port: 0, handleProtocols: () => "graphql-transport-ws" });
      let subscribed: { operationName?: string; query?: string } = {};
      wss.on("connection", (socket) => {
        socket.on("message", (raw) => {
          const msg = JSON.parse(String(raw)) as {
            type: string;
            id?: string;
            payload?: Record<string, unknown>;
          };
          if (msg.type === "connection_init") {
            socket.send(JSON.stringify({ type: "connection_ack" }));
          } else if (msg.type === "subscribe") {
            subscribed = msg.payload as typeof subscribed;
            socket.send(JSON.stringify({ id: msg.id, type: "next", payload: op.response }));
            socket.send(JSON.stringify({ id: msg.id, type: "complete" }));
          }
        });
      });
      await new Promise((resolve) => wss.once("listening", resolve));
      const client = createSubscriptionClient({
        url: `ws://127.0.0.1:${(wss.address() as AddressInfo).port}`,
        token: "t",
      });
      const events: unknown[] = [];
      try {
        for await (const data of client.subscribe(documentFor(op.name), op.variables)) {
          events.push(data);
        }
      } finally {
        await client.close();
        await new Promise((resolve) => wss.close(resolve));
      }
      expect(events).toEqual([op.response.data]);
      expect(subscribed.operationName).toBe(op.name);
      expect(subscribed.query).toBe(documentFor(op.name));
    });
  }
});
