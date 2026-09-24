import { readFileSync } from "node:fs";
import path from "node:path";
import { buildSchema, introspectionFromSchema } from "graphql";
import { beforeAll, describe, expect, it } from "vitest";

import {
  generateSdkSnippets,
  loadSdkCatalog,
  matchSdkOperation,
  pythonSnakeCase,
  type SdkCatalog,
  type SdkSnippetInput,
} from "./sdkSnippets";
import type { IntrospectedSchema } from "./schemaUtils";

const repoRoot = path.resolve(__dirname, "../../../../..");
const read = (rel: string) => readFileSync(path.join(repoRoot, rel), "utf8");

let catalog: SdkCatalog;
let schema: IntrospectedSchema;

beforeAll(async () => {
  catalog = await loadSdkCatalog();
  schema = introspectionFromSchema(buildSchema(read("pkg/graphql/public/schema.public.graphql")))
    .__schema as unknown as IntrospectedSchema;
});

function input(overrides: Partial<SdkSnippetInput>): SdkSnippetInput {
  return {
    query: "",
    variables: {},
    token: "YOUR_API_TOKEN",
    catalog,
    schema,
    httpUrl: "https://bridge.example.com/graphql",
    wsUrl: "wss://bridge.example.com/graphql/ws",
    docsUrl: "https://docs.example.com",
    ...overrides,
  };
}

describe("SDK catalog matches the generated SDKs", () => {
  it("covers every operation in the TypeScript SDK manifest", () => {
    const manifest = read("npm_api/src/generated/manifest.ts");
    const block = manifest.slice(manifest.indexOf("export const operations"));
    const names = [...block.matchAll(/^ {2}([A-Z][A-Za-z0-9]*): \{ kind:/gm)].map((m) => m[1]);
    expect(names.length).toBeGreaterThan(100);
    expect([...catalog.operations.keys()].sort()).toEqual(names.sort());
  });

  it("names a TypeScript document for every operation", () => {
    const ts = read("npm_api/src/generated/graphql.ts");
    for (const name of catalog.operations.keys()) {
      expect(ts, name).toContain(`export const ${name}Document `);
    }
  });

  it("takes Go arguments in variable order", () => {
    const go = read("sdk_go/generated.go");
    for (const op of catalog.operations.values()) {
      const vars = op.variables.map((v) => v.name);
      if (op.kind === "subscription") {
        const m = new RegExp(
          `^func Subscribe${op.name}\\(ctx context\\.Context, sc \\*SubscriptionClient(.*)\\) iter`,
          "m"
        ).exec(go);
        expect(m, op.name).not.toBeNull();
        const params = m![1]
          .split(",")
          .map((p) => p.trim().split(" ")[0])
          .filter(Boolean);
        expect(params, op.name).toEqual(vars);
      } else {
        const m = new RegExp(
          `^func ${op.name}\\(\\n\\tctx_ context\\.Context,\\n\\tclient_ graphql\\.Client,\\n([\\s\\S]*?)\\) \\(`,
          "m"
        ).exec(go);
        expect(m, op.name).not.toBeNull();
        const params = m![1]
          .split("\n")
          .map((l) => l.trim().split(" ")[0])
          .filter(Boolean);
        expect(params, op.name).toEqual(vars);
      }
    }
  });

  it("uses the Python SDK's method and keyword names", () => {
    const sync = read("sdk_python/src/livepeer_frameworks/_generated/graphql/client.py");
    const async_ = read("sdk_python/src/livepeer_frameworks/_generated/graphql/async_client.py");
    for (const op of catalog.operations.values()) {
      const source = op.kind === "subscription" ? async_ : sync;
      const method = pythonSnakeCase(op.name);
      const m = new RegExp(`def ${method}\\(\\s*self,?([\\s\\S]*?)\\*\\*kwargs`).exec(source);
      expect(m, `${op.name} -> ${method}`).not.toBeNull();
      const params = [...m![1].matchAll(/([a-z_][a-z0-9_]*):/g)].map((p) => p[1]).sort();
      expect(params, op.name).toEqual(op.variables.map((v) => pythonSnakeCase(v.name)).sort());
    }
  });

  it("reads the Python SDK's public input models", () => {
    expect(catalog.pythonExports.has("CreateStreamInput")).toBe(true);
    expect(catalog.pythonExports.has("BootstrapEdgeInput")).toBe(true);
    expect(catalog.pythonExports.has("WebhookDeliveryStatus")).toBe(true);
    expect(catalog.pythonExports.has("CreateStreamCreateStreamStream")).toBe(false);
  });
});

describe("matchSdkOperation", () => {
  it("matches by operation name and kind", () => {
    expect(
      matchSdkOperation("query GetStream($id: ID!) { stream(id: $id) { id } }", catalog)?.name
    ).toBe("GetStream");
    expect(matchSdkOperation("mutation GetStream { x }", catalog)).toBeNull();
    expect(matchSdkOperation("query MyStreams { streams { id } }", catalog)).toBeNull();
    expect(matchSdkOperation("{ me { id } }", catalog)).toBeNull();
    expect(matchSdkOperation("query {", catalog)).toBeNull();
  });
});

describe("generateSdkSnippets: SDK query", () => {
  const snippets = () =>
    generateSdkSnippets(
      input({
        query: "query GetStream($id: ID!) { stream(id: $id) { id name } }",
        variables: { id: "U3RyZWFtOjE=", extra: 1 },
      })
    );

  it("TypeScript requests the typed document", () => {
    const ts = snippets().tsSdk;
    expect(ts).toContain("// TypeScript SDK: npm install @livepeer-frameworks/api");
    expect(ts).toContain("// Guide: https://docs.example.com/builders/sdks");
    expect(ts).toContain(
      'import { createClient, GetStreamDocument } from "@livepeer-frameworks/api";'
    );
    expect(ts).toContain('url: "https://bridge.example.com/graphql"');
    expect(ts).toContain('client.request(GetStreamDocument, {\n  "id": "U3RyZWFtOjE="\n})');
    expect(ts).toContain("left out: extra");
  });

  it("Go calls the generated function", () => {
    const go = snippets().goSdk;
    expect(go).toContain("// Go SDK: go get github.com/Livepeer-FrameWorks/sdk-go");
    expect(go).toContain('frameworks "github.com/Livepeer-FrameWorks/sdk-go"');
    expect(go).toContain('resp, err := frameworks.GetStream(ctx, client, "U3RyZWFtOjE=")');
  });

  it("Python calls the snake_case method", () => {
    const py = snippets().pythonSdk;
    expect(py).toContain("# Python SDK: pip install livepeer-frameworks");
    expect(py).toContain(
      'with FrameWorksClient(\n    url="https://bridge.example.com/graphql",\n    token="YOUR_API_TOKEN",\n) as fw:'
    );
    expect(py).toContain('result = fw.get_stream(id="U3RyZWFtOjE=")');
  });
});

describe("generateSdkSnippets: SDK query with optional arguments", () => {
  it("passes Go pointers and leaves out unset Python keywords", () => {
    const out = generateSdkSnippets(
      input({
        query: "query GetApiUsageConnection($page: ConnectionInput) { x }",
        variables: { page: { first: 10 }, authType: "jwt" },
      })
    );
    expect(out.goSdk).toContain("frameworks.GetApiUsageConnection(");
    expect(out.goSdk).toContain("&frameworks.ConnectionInput{\n\t\t\tFirst: new(10),\n\t\t},");
    expect(out.goSdk).toContain('new("jwt"),\n\t\tnil,\n\t\tnil,\n\t\tnil,');
    expect(out.pythonSdk).toContain("from livepeer_frameworks.graphql import ConnectionInput");
    expect(out.pythonSdk).toContain(
      'page=ConnectionInput.model_validate(\n            {\n                "first": 10,\n            }\n        ),'
    );
    expect(out.pythonSdk).toContain('auth_type="jwt"');
    expect(out.pythonSdk).not.toContain("time_range");
  });
});

describe("generateSdkSnippets: SDK mutation", () => {
  const query =
    "mutation CreateStream($input: CreateStreamInput!) { createStream(input: $input) { __typename } }";
  const variables = { input: { name: "Launch", record: true, ingestMode: "PUSH" } };

  it("TypeScript passes the input object", () => {
    const ts = generateSdkSnippets(input({ query, variables })).tsSdk;
    expect(ts).toContain("client.request(CreateStreamDocument, {");
    expect(ts).toContain('"ingestMode": "PUSH"');
  });

  it("Go builds the input struct from the schema", () => {
    const go = generateSdkSnippets(input({ query, variables })).goSdk;
    expect(go).toContain(
      'frameworks.CreateStream(\n\t\tctx,\n\t\tclient,\n\t\tframeworks.CreateStreamInput{\n\t\t\tName:       "Launch",'
    );
    expect(go).toContain("Record:     new(true),");
    expect(go).toContain("IngestMode: new(frameworks.IngestModePush),");
  });

  it("Go decodes the input from JSON without a schema", () => {
    const go = generateSdkSnippets(input({ query, variables, schema: null })).goSdk;
    expect(go).toContain('"encoding/json"');
    expect(go).toContain("var input frameworks.CreateStreamInput");
    expect(go).toContain(
      'json.Unmarshal([]byte(`{"name":"Launch","record":true,"ingestMode":"PUSH"}`), &input)'
    );
    expect(go).toContain("frameworks.CreateStream(ctx, client, input)");
  });

  it("Python validates the exported input model", () => {
    const py = generateSdkSnippets(input({ query, variables })).pythonSdk;
    expect(py).toContain("from livepeer_frameworks.graphql import CreateStreamInput");
    expect(py).toContain(
      "fw.create_stream(\n        input=CreateStreamInput.model_validate(\n            {"
    );
  });

  it("fills a required variable the editor leaves out", () => {
    const out = generateSdkSnippets(input({ query, variables: {} }));
    // Required input fields get placeholders, so the call type-checks as generated.
    expect(out.goSdk).toContain(
      'frameworks.CreateStream(\n\t\tctx,\n\t\tclient,\n\t\tframeworks.CreateStreamInput{\n\t\t\tName: "",\n\t\t},\n\t)'
    );
    expect(out.tsSdk).toContain(
      'client.request(CreateStreamDocument, {\n  "input": {\n    "name": ""\n  }\n})'
    );
  });
});

describe("generateSdkSnippets: SDK subscription", () => {
  const out = () =>
    generateSdkSnippets(
      input({
        query:
          "subscription LiveStreamEvents($streamId: ID) { liveStreamEvents(streamId: $streamId) { type } }",
        variables: { streamId: "stream-1" },
      })
    );

  it("TypeScript uses the subscription client", () => {
    const ts = out().tsSdk;
    expect(ts).toContain("npm install @livepeer-frameworks/api graphql-ws");
    expect(ts).toContain(
      'import { createSubscriptionClient } from "@livepeer-frameworks/api/subscriptions";'
    );
    expect(ts).toContain('url: "wss://bridge.example.com/graphql/ws"');
    expect(ts).toContain("subscriptions.subscribe(LiveStreamEventsDocument, {");
  });

  it("Go ranges over the Subscribe iterator", () => {
    const go = out().goSdk;
    expect(go).toContain("frameworks.NewSubscriptionClient(frameworks.SubscriptionOptions{");
    expect(go).toContain(
      'for event, err := range frameworks.SubscribeLiveStreamEvents(ctx, sc, new("stream-1")) {'
    );
  });

  it("Python iterates the async client's method", () => {
    const py = out().pythonSdk;
    expect(py).toContain("from livepeer_frameworks import AsyncFrameWorksClient");
    expect(py).toContain('async for event in fw.live_stream_events(stream_id="stream-1"):');
    expect(py).not.toContain("ws_url=");
    expect(py).toContain("asyncio.run(main())");
  });

  it("Python passes ws_url when it is not the default", () => {
    const py = generateSdkSnippets(
      input({
        query: "subscription LiveFirehose { liveFirehose { type } }",
        wsUrl: "wss://realtime.example.com/ws",
      })
    ).pythonSdk;
    expect(py).toContain('ws_url="wss://realtime.example.com/ws"');
    expect(py).toContain("async for event in fw.live_firehose():");
  });
});

describe("generateSdkSnippets: custom document", () => {
  const query =
    "query MyStreams($first: Int) { streamsConnection(page: { first: $first }) { totalCount } }";
  const out = () => generateSdkSnippets(input({ query, variables: { first: 5 } }));

  it("TypeScript requests the document string", () => {
    const ts = out().tsSdk;
    expect(ts).toContain('import { createClient } from "@livepeer-frameworks/api";');
    expect(ts).toContain("const query = `query MyStreams");
    expect(ts).toContain('client.request(query, {\n  "first": 5\n})');
    expect(ts).toContain("No SDK operation has this document's name");
  });

  it("Go sends it through MakeRequest", () => {
    const go = out().goSdk;
    expect(go).toContain('"github.com/Khan/genqlient/graphql"');
    expect(go).toContain("err = client.MakeRequest(ctx, &graphql.Request{");
    expect(go).toContain('OpName: "MyStreams",');
    expect(go).toContain('Variables: map[string]any{\n\t\t\t"first": 5,\n\t\t},');
    expect(go).toContain("&graphql.Response{Data: &data}");
  });

  it("Python sends it through execute", () => {
    const py = out().pythonSdk;
    expect(py).toContain('query = """query MyStreams');
    expect(py).toContain('variables = {\n    "first": 5,\n}');
    expect(py).toContain(
      'response = fw.execute(query, operation_name="MyStreams", variables=variables)'
    );
    expect(py).toContain("print(fw.get_data(response))");
  });

  it("subscriptions use the raw subscription paths", () => {
    const sub = generateSdkSnippets(
      input({ query: "subscription MyEvents { liveFirehose { type } }" })
    );
    expect(sub.tsSdk).toContain("subscriptions.subscribe(query)");
    expect(sub.goSdk).toContain(
      'frameworks.Subscribe[map[string]any](ctx, sc, "MyEvents", query, nil)'
    );
    expect(sub.pythonSdk).toContain('fw.execute_ws(query, operation_name="MyEvents")');
  });
});

describe("pythonSnakeCase", () => {
  it("follows ariadne-codegen", () => {
    expect(pythonSnakeCase("GetNodeMetrics1hConnection")).toBe("get_node_metrics_1_h_connection");
    expect(pythonSnakeCase("SubmitX402Payment")).toBe("submit_x_402_payment");
    expect(pythonSnakeCase("streamId")).toBe("stream_id");
    expect(pythonSnakeCase("from")).toBe("from_");
  });
});
