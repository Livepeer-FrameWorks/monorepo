// Exercise the same generate_query -> execute_query tools that Skipper discovers.
const [url, token] = process.argv.slice(2);
let session;
let id = 0;
async function rpc(method, params, notification = false) {
  const response = await fetch(`${url.replace(/\/$/, "")}/mcp`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      accept: "application/json, text/event-stream",
      authorization: `Bearer ${token}`,
      "mcp-protocol-version": "2025-03-26",
      ...(session ? { "mcp-session-id": session } : {}),
    },
    body: JSON.stringify({ jsonrpc: "2.0", ...(notification ? {} : { id: ++id }), method, params }),
    signal: AbortSignal.timeout(30000),
  });
  session = response.headers.get("mcp-session-id") ?? session;
  if (!response.ok) throw new Error(`${method}: HTTP ${response.status}: ${await response.text()}`);
  if (notification) return;
  const text = await response.text();
  const message = text.trimStart().startsWith("{")
    ? JSON.parse(text)
    : text
        .split("\n")
        .filter((line) => line.startsWith("data:"))
        .map((line) => JSON.parse(line.slice(5)))
        .find((item) => item.id === id);
  if (!message || message.error || message.result?.isError)
    throw new Error(`${method}: ${JSON.stringify(message)}`);
  const result = message.result;
  return (
    result.structuredContent ??
    (result.content?.[0]?.text ? JSON.parse(result.content[0].text) : result)
  );
}
await rpc("initialize", {
  protocolVersion: "2025-03-26",
  capabilities: {},
  clientInfo: { name: "sdk-stack-smoke", version: "1" },
});
await rpc("notifications/initialized", {}, true);
const generated = await rpc("tools/call", {
  name: "generate_query",
  arguments: { field_name: "streamsConnection" },
});
if (/@(?:paginate|mask_disable)\b/.test(generated.query))
  throw new Error("client-only directives reached the MCP output");
if (Object.hasOwn(generated.variables, "first"))
  throw new Error("MCP supplied a value that overrides the GraphQL default");
const executed = await rpc("tools/call", {
  name: "execute_query",
  arguments: { query: generated.query, variables: generated.variables },
});
if (executed.errors?.length || typeof executed.data?.streamsConnection?.totalCount !== "number") {
  throw new Error(`generated query did not execute: ${JSON.stringify(executed)}`);
}
console.log(
  "PASS  sdk-mcp: generated streams query executes with the caller token and GraphQL defaults"
);
