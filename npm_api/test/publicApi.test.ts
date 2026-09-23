import { describe, expect, it } from "vitest";

import * as gatewayProbe from "../src/gatewayProbe.js";
import * as root from "../src/index.js";
import * as select from "../src/select.js";
import * as subscriptions from "../src/subscriptions.js";
import * as webhooks from "../src/webhooks.js";

// A change to these snapshots is a change to the package's public API; it
// needs a changeset whose bump matches it.
describe("public API", () => {
  it("root entry exports", () => {
    expect(Object.keys(root).sort()).toMatchSnapshot();
  });

  it("subscriptions entry exports", () => {
    expect(Object.keys(subscriptions).sort()).toMatchSnapshot();
  });

  it("select entry exports", () => {
    expect(Object.keys(select).sort()).toMatchSnapshot();
  });

  it("webhooks entry exports", () => {
    expect(Object.keys(webhooks).sort()).toMatchSnapshot();
  });

  it("gateway-probe entry exports", () => {
    expect(Object.keys(gatewayProbe).sort()).toMatchSnapshot();
  });

  it("operation manifest", () => {
    expect({
      line: root.sdkLine,
      minServer: root.minServerVersion,
      operations: root.operations,
    }).toMatchSnapshot();
  });
});
