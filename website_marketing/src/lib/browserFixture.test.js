import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { test } from "node:test";

import { runBrowserFixture } from "../../scripts/browser-fixture.mjs";

const fixtureUrl = "http://127.0.0.1:5173/@fs/header-and-headlines.html?width=390";
const networkChange = {
  error: "net::ERR_NETWORK_CHANGED",
  type: "script",
  url: "http://127.0.0.1:5173/src/components/Navigation.jsx",
};

function browserWithOutcomes(outcomes, clock) {
  const pages = [];
  const browser = {
    pages,
    async newPage() {
      assert.ok(
        pages.every((page) => page.closed),
        "dispose the old page before opening another"
      );
      const outcome = outcomes[pages.length];
      assert.ok(outcome, "no unbounded fixture attempts");
      const page = new EventEmitter();
      page.closed = false;
      page.timeouts = [];
      page.goto = async (_url, { timeout }) => {
        page.timeouts.push(["goto", timeout]);
        if (outcome.gotoElapsed) clock.tick(outcome.gotoElapsed);
        for (const request of outcome.requests || []) {
          page.emit("requestfailed", {
            failure: () => ({ errorText: request.error }),
            resourceType: () => request.type,
            url: () => request.url,
          });
        }
        if (outcome.gotoError) throw new Error(outcome.gotoError);
      };
      page.locator = () => ({
        async count() {
          return outcome.report ? 1 : 0;
        },
        async waitFor({ timeout }) {
          page.timeouts.push(["output", timeout]);
          if (outcome.elapsed) clock.tick(outcome.elapsed);
        },
        async getAttribute() {
          return outcome.status;
        },
        async textContent() {
          return outcome.report || "";
        },
      });
      page.close = async () => {
        page.closed = true;
      };
      pages.push(page);
      return page;
    },
  };
  return browser;
}

test("a diagnosed bootstrap network change replays once on a fresh page with the remaining budget", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 0 });
  const browser = browserWithOutcomes(
    [
      {
        status: "fail",
        report: "FAIL Header hydrated: primary navigation",
        requests: [networkChange],
        elapsed: 15_000,
      },
      { status: "pass", report: "PASS Header hydrated: primary navigation" },
    ],
    t.mock.timers
  );
  const originalFailures = [];
  const result = await runBrowserFixture(browser, fixtureUrl, (failure) =>
    originalFailures.push(failure)
  );
  assert.equal(result.status, "pass");
  assert.equal(browser.pages.length, 2);
  assert.ok(browser.pages.every((page) => page.closed));
  assert.equal(originalFailures.length, 1);
  assert.match(originalFailures[0].report, /FAIL Header hydrated/);
  assert.match(originalFailures[0].diagnostics[0], /net::ERR_NETWORK_CHANGED/);
  assert.deepEqual(browser.pages[1].timeouts, [
    ["goto", 30_000],
    ["output", 45_000],
  ]);
});

test("a second network-change failure fails without a third attempt", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 0 });
  const browser = browserWithOutcomes(
    [
      { status: "fail", requests: [networkChange] },
      { status: "fail", report: "FAIL Header hydrated again", requests: [networkChange] },
    ],
    t.mock.timers
  );
  let retries = 0;
  const result = await runBrowserFixture(browser, fixtureUrl, () => retries++);
  assert.equal(result.status, "fail");
  assert.equal(result.report, "FAIL Header hydrated again");
  assert.equal(browser.pages.length, 2);
  assert.equal(retries, 1);
  assert.ok(browser.pages.every((page) => page.closed));
});

test("navigation and fixture waiting consume the same original case budget", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 0 });
  const browser = browserWithOutcomes(
    [
      {
        status: "fail",
        requests: [networkChange],
        gotoElapsed: 20_000,
        elapsed: 15_000,
      },
      { status: "pass" },
    ],
    t.mock.timers
  );
  const result = await runBrowserFixture(browser, fixtureUrl, () => {});
  assert.equal(result.status, "pass");
  assert.deepEqual(browser.pages[0].timeouts, [
    ["goto", 30_000],
    ["output", 40_000],
  ]);
  assert.deepEqual(browser.pages[1].timeouts, [
    ["goto", 25_000],
    ["output", 25_000],
  ]);
});

test("ordinary UI failures and unrelated transport failures are never retried", async (t) => {
  for (const [name, request] of [
    ["UI assertion", null],
    ["other transport", { ...networkChange, error: "net::ERR_CONNECTION_REFUSED" }],
    ["nonexact error", { ...networkChange, error: "net::ERR_NETWORK_CHANGED additional text" }],
    ["external script", { ...networkChange, url: "https://other.example/client.js" }],
    ["other local origin", { ...networkChange, url: "http://127.0.0.1:5174/client.js" }],
    ["image", { ...networkChange, type: "image" }],
    ["API request", { ...networkChange, type: "fetch" }],
  ]) {
    await t.test(name, async (t) => {
      t.mock.timers.enable({ apis: ["Date"], now: 0 });
      const browser = browserWithOutcomes(
        [{ status: "fail", report: "FAIL actual assertion", requests: request ? [request] : [] }],
        t.mock.timers
      );
      const result = await runBrowserFixture(browser, fixtureUrl, () =>
        assert.fail("unexpected retry")
      );
      assert.equal(result.status, "fail");
      assert.equal(browser.pages.length, 1);
      assert.ok(browser.pages[0].closed);
    });
  }
});

test("an exhausted original case budget prevents even a diagnosed transport replay", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 0 });
  const browser = browserWithOutcomes(
    [{ status: "fail", requests: [networkChange], elapsed: 60_000 }],
    t.mock.timers
  );
  const result = await runBrowserFixture(browser, fixtureUrl, () =>
    assert.fail("budget exhausted")
  );
  assert.equal(result.status, "fail");
  assert.equal(browser.pages.length, 1);
  assert.ok(browser.pages[0].closed);
});

test("a passing case is not replayed even when an unrelated request changed networks", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 0 });
  const browser = browserWithOutcomes(
    [{ status: "pass", requests: [networkChange] }],
    t.mock.timers
  );
  const result = await runBrowserFixture(browser, fixtureUrl, () => assert.fail("passing case"));
  assert.equal(result.status, "pass");
  assert.equal(browser.pages.length, 1);
  assert.ok(browser.pages[0].closed);
});

test("a failed document navigation can recover and disposes both pages", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 0 });
  const browser = browserWithOutcomes(
    [
      {
        gotoError: "page.goto: net::ERR_NETWORK_CHANGED",
        requests: [{ ...networkChange, type: "document", url: fixtureUrl }],
      },
      { status: "pass" },
    ],
    t.mock.timers
  );
  const result = await runBrowserFixture(browser, fixtureUrl, () => {});
  assert.equal(result.status, "pass");
  assert.equal(browser.pages.length, 2);
  assert.ok(browser.pages.every((page) => page.closed));
});
