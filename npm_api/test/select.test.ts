import { readFileSync } from "node:fs";
import type { AddressInfo } from "node:net";
import { fileURLToPath } from "node:url";

import { buildSchema, parse, validate } from "graphql";
import { describe, expect, expectTypeOf, it } from "vitest";
import { WebSocketServer } from "ws";

import { createClientWith } from "../src/client.js";
import { AuthenticationError, NetworkError } from "../src/errors.js";
import { buildOperation, createSelectClient } from "../src/select.js";
import { createSubscriptionClient } from "../src/subscriptions.js";
import { type FixtureResponse, scriptedFetch } from "./fixtures.js";

const url = "https://select.test/graphql";

function selectClient(
  queues: Record<string, FixtureResponse[]>,
  options: { token?: () => string; checkServer?: boolean } = {}
) {
  const scripted = scriptedFetch(queues);
  const delays: number[] = [];
  const client = createClientWith(
    {
      url,
      fetch: scripted.fetch,
      token: options.token,
      checkServer: options.checkServer ?? false,
      retry: { jitter: false },
    },
    { sleep: async (ms) => void delays.push(ms) }
  );
  return { select: createSelectClient(client), requests: scripted.requests, delays };
}

describe("select: building operations", () => {
  it("turns arguments into typed variables and union members into fragments", () => {
    const op = buildOperation("mutation", {
      __name: "NewStream",
      createStream: {
        __args: { input: { name: "Launch" } },
        __typename: true,
        on_Stream: { id: true },
        on_ValidationError: { message: true },
      },
    });
    expect(op.operationName).toBe("NewStream");
    expect(op.variables).toEqual({ v1: { name: "Launch" } });
    expect(op.query).toMatch(/^mutation NewStream\(\$v1:CreateStreamInput!\)/);
    expect(op.query).toContain("fragment f1 on Stream{id}");
    expect(op.query).toContain("fragment f2 on ValidationError{message}");
  });

  it("builds documents that validate against the public schema", () => {
    const schema = buildSchema(
      readFileSync(
        fileURLToPath(new URL("../../pkg/graphql/public/schema.public.graphql", import.meta.url)),
        "utf8"
      )
    );
    const ops = [
      buildOperation("query", {
        node: { __args: { id: "n1" }, __typename: true, on_Stream: { __scalar: true } },
        balanceTransactionsConnection: {
          __args: { page: { first: 5 } },
          edges: { cursor: true, node: { id: true, createdAt: true } },
          pageInfo: { hasNextPage: true },
        },
      }),
      buildOperation("mutation", {
        createStream: {
          __args: { input: { name: "Launch" } },
          on_Stream: { id: true },
          on_Error: { message: true },
        },
      }),
      buildOperation("subscription", {
        tenantEvents: { __args: { types: ["stream.live"] }, id: true, data: { __typename: true } },
      }),
    ];
    for (const op of ops) {
      expect(validate(schema, parse(op.query)).map((e) => e.message)).toEqual([]);
    }
  });

  it("rejects a field the schema does not have before sending anything", () => {
    // A selection held in a variable escapes TypeScript's excess property check.
    const selection = {
      tenantEvents: { data: { on_StreamLive: { streamId: true, currentViewers: true } } },
    };
    expect(() => buildOperation("subscription", selection)).toThrow(
      "Subscription.tenantEvents.data.on_StreamLive: StreamLive has no field currentViewers"
    );
  });

  it("refuses an SDK operation name, whose release the version check would borrow", () => {
    expect(() => buildOperation("query", { __name: "GetStream", tenant: { id: true } })).toThrow(
      /SDK operation name/
    );
  });
});

describe("select: queries and mutations over the SDK transport", () => {
  it("selects union members and narrows the result by __typename", async () => {
    const { select, requests } = selectClient({
      "*": [
        {
          body: {
            data: {
              createStream: { __typename: "ValidationError", message: "name is taken" },
            },
          },
        },
      ],
    });
    const data = await select.mutation({
      createStream: {
        __args: { input: { name: "Launch" } },
        __typename: true,
        on_Stream: { id: true, streamKey: true },
        on_ValidationError: { message: true, field: true },
      },
    });
    const result = data.createStream;
    if (result.__typename === "ValidationError") {
      expectTypeOf(result.message).toEqualTypeOf<string>();
      expect(result.message).toBe("name is taken");
    } else {
      expect.unreachable(`unexpected ${result.__typename}`);
    }
    expect(requests).toHaveLength(1);
    expect(requests[0]?.body.variables).toEqual({ v1: { name: "Launch" } });
  });

  it("round-trips Time and JSON scalars as the generated documents type them", async () => {
    const start = "2026-09-01T00:00:00Z";
    const end = "2026-09-02T00:00:00Z";
    const payload = { buffer: "FULL", tracks: [1, 2] };
    const { select, requests } = selectClient({
      "*": [
        {
          body: {
            data: {
              balanceTransactionsConnection: { nodes: [{ id: "t1", createdAt: start }] },
              node: { __typename: "BufferEvent", timestamp: end, payload },
            },
          },
        },
      ],
    });
    const data = await select.query({
      balanceTransactionsConnection: {
        __args: { timeRange: { start, end } },
        nodes: { id: true, createdAt: true },
      },
      node: {
        __args: { id: "buffer-event-1" },
        __typename: true,
        on_BufferEvent: { timestamp: true, payload: true },
      },
    });
    expect(requests[0]?.body.variables).toEqual({
      v1: { start, end },
      v2: "buffer-event-1",
    });
    expect(requests[0]?.body.query).toContain("$v1:TimeRangeInput");
    const node = data.balanceTransactionsConnection.nodes[0];
    expectTypeOf(data.balanceTransactionsConnection.nodes[0]!.createdAt).toEqualTypeOf<string>();
    expect(node?.createdAt).toBe(start);
    const event = data.node;
    if (event?.__typename !== "BufferEvent") {
      expect.unreachable("node is not a BufferEvent");
      return;
    }
    expectTypeOf(event.timestamp).toEqualTypeOf<string>();
    expectTypeOf(event.payload).toEqualTypeOf<unknown>();
    expect(event.timestamp).toBe(end);
    expect(event.payload).toEqual(payload);
  });

  it("reads the token for every request and raises AuthenticationError on 401", async () => {
    const tokens = ["expired", "fresh"];
    let current = tokens[0]!;
    const { select, requests } = selectClient(
      {
        "*": [
          { status: 401, body: { message: "token expired" } },
          { body: { data: { tenant: null } } },
        ],
      },
      { token: () => current }
    );
    const failed = await select.query({ tenant: { id: true } }).then(
      () => null,
      (err: unknown) => err
    );
    expect(failed).toBeInstanceOf(AuthenticationError);
    expect((failed as AuthenticationError).status).toBe(401);
    current = tokens[1]!;
    await expect(select.query({ tenant: { id: true } })).resolves.toEqual({ tenant: null });
    expect(requests.map((r) => r.headers.authorization)).toEqual([
      "Bearer expired",
      "Bearer fresh",
    ]);
  });

  it("maps GraphQL UNAUTHORIZED errors to AuthenticationError", async () => {
    const { select } = selectClient({
      "*": [
        {
          body: {
            errors: [{ message: "not signed in", extensions: { code: "UNAUTHORIZED" } }],
            data: null,
          },
        },
      ],
    });
    await expect(select.query({ tenant: { id: true } })).rejects.toBeInstanceOf(
      AuthenticationError
    );
  });

  it("does not send a mutation again once it may have reached the server", async () => {
    const { select, requests } = selectClient({ "*": [{ networkError: "reset" }] });
    await expect(
      select.mutation({ deleteStream: { __args: { id: "s1" }, __typename: true } })
    ).rejects.toBeInstanceOf(NetworkError);
    expect(requests).toHaveLength(1);
  });

  it("sends a mutation again when the connection was refused before sending", async () => {
    const { select, requests, delays } = selectClient({
      "*": [
        { networkError: "refused" },
        { body: { data: { deleteStream: { __typename: "DeleteSuccess" } } } },
      ],
    });
    await expect(
      select.mutation(
        { deleteStream: { __args: { id: "s1" }, __typename: true } },
        { idempotencyKey: "delete-s1" }
      )
    ).resolves.toEqual({ deleteStream: { __typename: "DeleteSuccess" } });
    expect(requests).toHaveLength(2);
    expect(delays).toEqual([250]);
    expect(requests.map((r) => r.headers["idempotency-key"])).toEqual(["delete-s1", "delete-s1"]);
  });

  it("checks the server version before a mutation", async () => {
    const { select, requests } = selectClient(
      {
        ServerInfo: [{ body: { data: { serverInfo: { version: "v0.3.11", features: [] } } } }],
        "*": [{ body: { data: { deleteStream: { __typename: "DeleteSuccess" } } } }],
      },
      { checkServer: true }
    );
    await select.mutation({ deleteStream: { __args: { id: "s1" }, __typename: true } });
    expect(requests.map((r) => r.operationName)).toEqual(["ServerInfo", null]);
  });
});

describe("select: subscriptions", () => {
  it("runs a subscription selection on the SDK's subscription client", async () => {
    let received: { query: string; variables: unknown } | null = null;
    const wss = new WebSocketServer({
      port: 0,
      handleProtocols: (protocols) =>
        protocols.has("graphql-transport-ws") ? "graphql-transport-ws" : false,
    });
    wss.on("connection", (socket) => {
      socket.on("message", (raw) => {
        const msg = JSON.parse(String(raw)) as {
          type: string;
          id?: string;
          payload?: { query: string; variables: unknown };
        };
        if (msg.type === "connection_init") {
          socket.send(JSON.stringify({ type: "connection_ack" }));
          return;
        }
        if (msg.type === "subscribe" && msg.payload) {
          received = msg.payload;
          const data = {
            tenantEvents: {
              id: "evt-1",
              time: "2026-09-23T12:00:00Z",
              data: { __typename: "StreamLive", streamId: "s1" },
            },
          };
          socket.send(JSON.stringify({ id: msg.id, type: "next", payload: { data } }));
          socket.send(JSON.stringify({ id: msg.id, type: "complete" }));
        }
      });
    });
    await new Promise((resolve) => wss.once("listening", resolve));
    const subscriptions = createSubscriptionClient({
      url: `ws://127.0.0.1:${(wss.address() as AddressInfo).port}`,
      token: "tok",
    });
    const { fetch } = scriptedFetch({});
    const select = createSelectClient(createClientWith({ url, fetch, checkServer: false }, {}), {
      subscriptions,
    });
    const live: string[] = [];
    try {
      for await (const event of select.subscription({
        tenantEvents: {
          __args: { streamId: "s1" },
          id: true,
          time: true,
          data: {
            __typename: true,
            on_StreamLive: { streamId: true },
          },
        },
      })) {
        expectTypeOf(event.tenantEvents.time).toEqualTypeOf<string>();
        const data = event.tenantEvents.data;
        if (data.__typename === "StreamLive") {
          live.push(data.streamId);
        }
      }
    } finally {
      await subscriptions.close();
      await new Promise((resolve) => wss.close(resolve));
    }
    expect(live).toEqual(["s1"]);
    expect(received).not.toBeNull();
    expect(received!.query).toMatch(/^subscription \(\$v1:ID\)\{tenantEvents\(streamId:\$v1\)/);
    expect(received!.variables).toEqual({ v1: "s1" });
  });

  it("needs a subscription client for subscription selections", async () => {
    const { select } = selectClient({});
    const events = select.subscription({ liveSystemHealth: { __typename: true } });
    await expect(events.next()).rejects.toThrow(/SubscriptionClient/);
  });
});
