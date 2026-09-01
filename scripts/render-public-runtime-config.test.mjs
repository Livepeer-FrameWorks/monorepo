import assert from "node:assert/strict";
import test from "node:test";

import { renderRuntimeConfig } from "./render-public-runtime-config.mjs";

test("renders only the allowlisted CARTO fields", () => {
  const output = renderRuntimeConfig({
    BASEMAP_PROVIDER: "carto",
    BASEMAP_STYLE: "dark-matter",
    CARTO_BASEMAP_BROWSER_KEY: "synthetic-key",
    DATABASE_PASSWORD: "must-not-leak",
  });

  assert.match(output, /synthetic-key/);
  assert.doesNotMatch(output, /DATABASE_PASSWORD|must-not-leak/);
  assert.match(output, /\"version\":1/);
});

test("serializes hostile browser-visible values without creating executable markup", () => {
  const output = renderRuntimeConfig({
    BASEMAP_PROVIDER: "carto",
    BASEMAP_STYLE: "dark-matter",
    CARTO_BASEMAP_BROWSER_KEY: "x\u2028y\u2029z</script><script>alert(1)</script>&",
  });

  assert.doesNotMatch(output, /<|>|&|\u2028|\u2029/u);
  assert.match(output, /\\u003c\/script\\u003e/);
  assert.match(output, /x\\u2028y\\u2029z/);
});

test("renders OpenFreeMap without forwarding a CARTO key", () => {
  const output = renderRuntimeConfig({
    BASEMAP_PROVIDER: "openfreemap",
    BASEMAP_STYLE: "dark",
    CARTO_BASEMAP_BROWSER_KEY: "must-not-leak",
  });

  assert.doesNotMatch(output, /must-not-leak|\"key\"/);
});

test("rejects unsupported or incomplete configurations", () => {
  assert.throws(
    () => renderRuntimeConfig({ BASEMAP_PROVIDER: "carto", BASEMAP_STYLE: "dark-matter" }),
    /CARTO_BASEMAP_BROWSER_KEY/
  );
  assert.throws(
    () => renderRuntimeConfig({ BASEMAP_PROVIDER: "carto", BASEMAP_STYLE: "voyager" }),
    /must be carto\/dark-matter or openfreemap\/dark/
  );
});
