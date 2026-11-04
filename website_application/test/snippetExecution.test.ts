import { createServer } from "node:http";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { execFile, spawnSync } from "node:child_process";
import { promisify } from "node:util";
import { Kind, parse } from "graphql";
import { afterAll, beforeAll, expect, it } from "vitest";
import { createClient } from "../../npm_api/src/client";
import { getSdk } from "../../npm_api/src/generated/methods";
import {
  generateSdkSnippets,
  loadSdkCatalog,
  type SdkCatalog,
} from "../src/lib/graphql/services/sdkSnippets";
import { explorerService } from "../src/lib/graphql/services/explorer";

const run = promisify(execFile);
const directory = mkdtempSync(path.join(tmpdir(), "sdk-snippets-"));
const query =
  "mutation CreateStream($input: CreateStreamInput!) { createStream(input: $input) { __typename } }";
const variables = {
  input: {
    name: "apostrophe ' quote \" backtick ` ${expression} slash \\ café😀\nline",
    record: true,
    description: null,
  },
};
let catalog: SdkCatalog;
beforeAll(async () => {
  catalog = await loadSdkCatalog();
});
afterAll(() => {
  rmSync(directory, { recursive: true, force: true });
});
const withoutImports = (code: string) => code.replace(/^import .*;\n/gm, "");
async function execute(code: string, scope: Record<string, unknown>): Promise<void> {
  await new Function(...Object.keys(scope), `return (async () => { ${withoutImports(code)} })()`)(
    ...Object.values(scope)
  );
}

it("executes the TS SDK export without changing mutation inputs or document", async () => {
  const snippet = generateSdkSnippets({
    query,
    variables,
    catalog,
    token: "test",
    httpUrl: "https://snippet.test/graphql",
    wsUrl: "wss://snippet.test/graphql/ws",
    docsUrl: "https://docs.test",
  });
  let sent: unknown;
  await execute(snippet.tsSdk, {
    createClient: (options: Parameters<typeof createClient>[0]) =>
      createClient({
        ...options,
        checkServer: false,
        fetch: async (_url, init) => {
          sent = JSON.parse(String(init?.body));
          return new Response(JSON.stringify({ data: {} }));
        },
      }),
    getSdk,
    console: { log() {} },
  });
  expect(sent).toEqual({ query, variables, operationName: "CreateStream" });
});

it("executes the fetch export with the original JSON payload", async () => {
  const code = explorerService.generateCodeExamples(query, variables).fetch;
  let sent: unknown;
  await execute(code, {
    fetch: async (_url: string, init: RequestInit) => {
      sent = JSON.parse(String(init.body));
      return new Response(JSON.stringify({ data: {} }));
    },
    console: { log() {} },
  });
  expect(sent).toEqual({ query, variables, operationName: "CreateStream" });
});

it("executes the Python export with nested booleans, nulls, and escaped strings", () => {
  const code = explorerService.generateCodeExamples(query, variables).python;
  const fixture = `import sys, types, json
class Response:
    def raise_for_status(self): pass
    def json(self): return {}
def post(*args, **kwargs):
    print(json.dumps(kwargs["json"], ensure_ascii=False))
    return Response()
sys.modules["requests"] = types.SimpleNamespace(post=post)
`;
  const result = spawnSync("python3", ["-c", fixture + code], { encoding: "utf8" });
  expect(result.status, result.stderr).toBe(0);
  expect(JSON.parse(result.stdout.split("\n")[0])).toEqual({
    query,
    variables,
    operationName: "CreateStream",
  });
});

it("executes the Go export against a local request recorder", async () => {
  let sent: unknown;
  const server = createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => {
      body += String(chunk);
    });
    request.on("end", () => {
      sent = JSON.parse(body);
      response.end('{"data":{}}');
    });
  });
  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", resolve);
  });
  try {
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("missing recorder address");
    const code = explorerService
      .generateCodeExamples(query, variables)
      .go.replace(
        /http.NewRequest\("POST", "[^"]*",/,
        `http.NewRequest("POST", "http://127.0.0.1:${address.port}",`
      );
    const file = path.join(directory, "main.go");
    writeFileSync(file, code);
    await run(
      "make",
      [
        "--no-print-directory",
        "-C",
        path.resolve(".."),
        "test-sdk-go-snippet",
        `SDK_SNIPPET_FILE=${file}`,
      ],
      { timeout: 120000 }
    );
    expect(sent).toEqual({ query, variables, operationName: "CreateStream" });
  } finally {
    await new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error) reject(error);
        else resolve();
      });
    });
  }
}, 125000);

it.each([
  { name: "raw null fields", query, variables, typed: false },
  { name: "BOM in document", query: query.replace(" {", " \ufeff{"), variables, typed: false },
  { name: "lone surrogate", query, variables: { input: { name: "\ud800" } }, typed: false },
  {
    name: "large Float",
    query: "GetDVRChapter",
    variables: { dvrId: "test", startMs: 1e21, endMs: 2e21 },
    typed: true,
  },
  {
    name: "typed lone surrogate",
    query: "CreateStream",
    variables: { input: { name: "\ud800" } },
    typed: false,
  },
])(
  "executes the Go SDK export: $name",
  async (fixture) => {
    const document = catalog.operations.get(fixture.query)?.document ?? fixture.query;
    let sent: unknown;
    const server = createServer((request, response) => {
      let body = "";
      request.on("data", (chunk) => {
        body += String(chunk);
      });
      request.on("end", () => {
        const payload = JSON.parse(body);
        if (payload.operationName === "ServerInfo")
          response.end('{"data":{"serverInfo":{"version":"v99.0.0","features":[]}}}');
        else {
          sent = payload;
          response.end('{"data":{}}');
        }
      });
    });
    await new Promise<void>((resolve) => {
      server.listen(0, "127.0.0.1", resolve);
    });
    try {
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("missing recorder address");
      const snippet = generateSdkSnippets({
        query: document,
        variables: fixture.variables,
        catalog,
        token: "test",
        httpUrl: `http://127.0.0.1:${address.port}`,
        wsUrl: "wss://unused.test",
        docsUrl: "https://docs.test",
      });
      const file = path.join(directory, "sdk.go");
      writeFileSync(file, snippet.goSdk);
      await run(
        "make",
        [
          "--no-print-directory",
          "-C",
          path.resolve(".."),
          "test-sdk-go-snippet",
          `SDK_SNIPPET_FILE=${file}`,
        ],
        { timeout: 120000 }
      );
      expect(sent).toMatchObject({
        variables: fixture.variables,
        operationName: fixture.name === "large Float" ? "GetDVRChapter" : "CreateStream",
      });
      if (!fixture.typed) expect(sent).toHaveProperty("query", document);
    } finally {
      await new Promise<void>((resolve, reject) => {
        server.close((error) => {
          if (error) reject(error);
          else resolve();
        });
      });
    }
  },
  125000
);

it("emits Apollo 4 setup and mutation dispatch consistently", () => {
  const code = explorerService.generateCodeExamples(query, variables).javascript;
  expect(code).toContain("link: new HttpLink(");
  expect(code).toContain("client.mutate(");
  expect(code).toContain("@apollo/client@^4 graphql rxjs");
});

it("preserves own __proto__ variables in the Apollo export", async () => {
  const variables = JSON.parse(
    '{"input":{"__proto__":{"kept":true},"name":"test"},"__proto__":"keep"}'
  );
  const code = explorerService.generateCodeExamples(query, variables).javascript;
  let sent: unknown;
  await execute(code, {
    ApolloClient: class {
      async mutate(options: { variables: unknown }) {
        sent = options.variables;
        return { data: {} };
      }
    },
    InMemoryCache: class {},
    HttpLink: class {},
    gql: parse,
    console: { log() {} },
  });
  expect(JSON.stringify(sent)).toBe(JSON.stringify(variables));
});

it("preserves own __proto__ variables in the TS SDK export", async () => {
  const variables = JSON.parse(
    '{"input":{"__proto__":{"kept":true},"name":"test"},"__proto__":"keep"}'
  );
  const code = generateSdkSnippets({
    query,
    variables,
    catalog,
    token: "test",
    httpUrl: "https://snippet.test",
    wsUrl: "wss://snippet.test",
    docsUrl: "https://docs.test",
  }).tsSdk;
  let sent: unknown;
  await execute(code, {
    createClient: (options: Parameters<typeof createClient>[0]) =>
      createClient({
        ...options,
        checkServer: false,
        fetch: async (_url, init) => {
          sent = JSON.parse(String(init?.body)).variables;
          return new Response('{"data":{}}');
        },
      }),
    getSdk,
    console: { log() {} },
  });
  expect(JSON.stringify(sent)).toBe(JSON.stringify(variables));
});

it("exports only the explicitly selected operation and its fragments to Apollo", async () => {
  const document = `query First { tenant { id } }
query Second { tenant { ...TenantName } }
fragment TenantName on Tenant { name }
fragment Unused on Tenant { id }`;
  const code = explorerService.generateCodeExamples(document, {}, null, {
    catalog: null,
    docsUrl: "https://docs.test",
    operationName: "Second",
  }).javascript;
  let requested: unknown;
  await execute(code, {
    ApolloClient: class {
      async query(options: Record<string, unknown>) {
        requested = options;
        return { data: {} };
      }
    },
    InMemoryCache: class {},
    HttpLink: class {},
    gql: (source: string) => {
      const parsed = parse(source);
      const operations = parsed.definitions.filter(
        (item) => item.kind === Kind.OPERATION_DEFINITION
      );
      expect(operations).toHaveLength(1);
      expect(operations[0].name?.value).toBe("Second");
      expect(parsed.definitions).toHaveLength(2);
      return parsed;
    },
    console: { log() {} },
  });
  expect(requested).not.toHaveProperty("operationName");
});
