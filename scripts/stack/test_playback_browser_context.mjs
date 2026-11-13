import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { createPlaybackBrowserPage } from "./playback-browser-context.mjs";

const require = createRequire(new URL("../../website_marketing/package.json", import.meta.url));
const { chromium } = require("playwright");
const requests = [];
const server = createServer((request, response) => {
  requests.push(request.headers);
  response.writeHead(200, {
    ...(request.url === "/no-cors" ? {} : { "Access-Control-Allow-Origin": "*" }),
    "Content-Type": "text/plain",
  });
  response.end("stack-media-network-fixture");
});
await new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, "127.0.0.1", resolve);
});
let browser;
try {
  browser = await chromium.launch({
    headless: true,
    args: ["--host-resolver-rules=MAP foghorn 127.0.0.1"],
  });
  const { context, page, origin } = await createPlaybackBrowserPage(browser);
  const target = `http://127.0.0.1:${server.address().port}/manifest`;
  const body = await page.evaluate(async (url) => (await fetch(url)).text(), target);
  assert.equal(body, "stack-media-network-fixture", "fixture must reach private-network media");
  assert.equal(requests.at(-1).origin, origin);
  assert.equal(requests.at(-1).cookie, undefined, "public media fixture must send no cookies");
  const namedTarget = `http://foghorn:${server.address().port}/manifest`;
  const namedBody = await page.evaluate(async (url) => (await fetch(url)).text(), namedTarget);
  assert.equal(namedBody, body, "fixture must reach HTTP media addressed by a stack hostname");
  assert.equal(await page.evaluate(() => isSecureContext), true);
  const corsBlocked = await page.evaluate(async (url) => {
    try {
      await fetch(url);
      return false;
    } catch {
      return true;
    }
  }, `http://foghorn:${server.address().port}/no-cors`);
  assert.equal(corsBlocked, true, "fixture must still enforce cross-origin response headers");

  const foreignOrigin = "https://another-player.frameworks.test";
  const foreign = await context.newPage();
  await foreign.route(`${foreignOrigin}/`, (route) =>
    route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Other origin</title>" })
  );
  await foreign.goto(`${foreignOrigin}/`);
  const denied = await foreign.evaluate(async (url) => {
    try {
      await fetch(url);
      return false;
    } catch {
      return true;
    }
  }, target);
  assert.equal(denied, true, "private-network permission must stay scoped to the fixture origin");
  await context.close();
  console.log(
    "PASS named private media works while CORS and other-origin restrictions stay enforced"
  );
} finally {
  await browser?.close();
  await new Promise((resolve) => server.close(resolve));
}
