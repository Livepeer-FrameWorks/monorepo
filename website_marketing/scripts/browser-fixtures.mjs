// Runs the layout fixtures in test/browser/ against the dev server in headless
// Chromium. Each fixture loads a real route in an iframe at a fixed width and
// sets data-status="pass" or "fail" on its <output>; any fail, or a fixture
// that never reports, fails the run.
//
// usage: node scripts/browser-fixtures.mjs   (needs `playwright install chromium`)

import { spawn } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:net";
import { join } from "node:path";
import { chromium } from "playwright";

const siteDir = join(import.meta.dirname, "..");
const fixtureDir = join(siteDir, "test", "browser");

// Fixture runs that need no running backend. The Livepeer explorer view
// (network-layout.html?view=network) waits for observation data, so it stays a
// manual check against a stack.
const cases = [
  ["header-and-headlines.html", "width=390"],
  ["header-and-headlines.html", "width=1280"],
  ["header-and-headlines.html", "width=1440"],
  ["network-layout.html", "width=390"],
  ["network-layout.html", "width=768"],
  ["network-layout.html", "width=1280"],
  ["skipper-height.html", "width=390"],
  ["skipper-height.html", "width=1280"],
];

const freePort = () =>
  new Promise((resolve, reject) => {
    const server = createServer();
    server.unref();
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });

async function waitForServer(url, child, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`dev server exited with ${child.exitCode}`);
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {
      // not listening yet
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`dev server did not answer ${url} within ${timeoutMs / 1000}s`);
}

const port = await freePort();
const origin = `http://127.0.0.1:${port}`;
const devServer = spawn("pnpm", ["exec", "react-router", "dev"], {
  cwd: siteDir,
  env: {
    ...process.env,
    HOST: "127.0.0.1",
    PORT: String(port),
    // Nothing listens here: the fixtures check layout, and API calls fail fast.
    DEV_PROXY_GATEWAY_URL: "http://127.0.0.1:9",
    VITE_GRAPHQL_HTTP_URL: "/graphql",
    VITE_MCP_URL: "/mcp",
  },
  stdio: ["ignore", "pipe", "pipe"],
  detached: true,
});
let serverLog = "";
devServer.stdout.on("data", (chunk) => (serverLog += chunk));
devServer.stderr.on("data", (chunk) => (serverLog += chunk));

const stopServer = () => {
  if (devServer.exitCode === null) {
    try {
      process.kill(-devServer.pid, "SIGTERM");
    } catch {
      devServer.kill("SIGTERM");
    }
  }
};
process.on("exit", stopServer);

let failed = 0;
try {
  await waitForServer(`${origin}/`, devServer, 120_000);
  const browser = await chromium.launch();
  try {
    for (const [fixture, query] of cases) {
      const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
      const url = `${origin}/@fs${join(fixtureDir, fixture)}?${query}`;
      let status = "timeout";
      let report = "";
      try {
        await page.goto(url);
        const output = page.locator('output[data-status="pass"], output[data-status="fail"]');
        await output.waitFor({ timeout: 60_000 });
        status = await output.getAttribute("data-status");
        report = await output.textContent();
      } catch (error) {
        report =
          (await page
            .locator("output")
            .textContent()
            .catch(() => "")) || error.message;
      } finally {
        await page.close();
      }
      if (status !== "pass") failed += 1;
      console.log(`${status === "pass" ? "PASS" : "FAIL"} ${fixture}?${query} (${status})`);
      for (const line of String(report).trim().split("\n")) console.log(`    ${line}`);
    }
  } finally {
    await browser.close();
  }
} catch (error) {
  failed += 1;
  console.error(`browser fixtures could not run: ${error.message}`);
  console.error(serverLog.slice(-4000));
} finally {
  stopServer();
}

if (failed > 0) {
  console.error(`${failed} browser fixture run(s) failed`);
  process.exit(1);
}
console.log(`All ${cases.length} browser fixture runs passed.`);
