import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  OBSERVATIONS_NOT_CONFIGURED_NOTE,
  observationsFailure,
} from "../../../packages/map-core/network-explorer.ts";

const dashboardSource = readFileSync(
  new URL("../../../packages/map-core/network-dashboard.ts", import.meta.url),
  "utf8"
);

// Bridge answers NETWORK_OBSERVATIONS_NOT_CONFIGURED both without an observer and
// without a valid network scope (api_gateway/internal/handlers/network.go).
test("the network page note fits every not-configured cause", () => {
  for (const error of [
    "network observations are not configured",
    "network scope is not configured",
  ]) {
    assert.equal(
      observationsFailure(503, { error, code: "NETWORK_OBSERVATIONS_NOT_CONFIGURED" })
        .notConfigured,
      true
    );
  }
  assert.doesNotMatch(OBSERVATIONS_NOT_CONFIGURED_NOTE, /without the Livepeer network observer/);
  assert.match(OBSERVATIONS_NOT_CONFIGURED_NOTE, /does not publish Livepeer network observations/);
  assert.doesNotMatch(dashboardSource, /runs without the Livepeer network observer/);
});
