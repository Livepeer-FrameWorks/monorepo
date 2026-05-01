import { describe, expect, it } from "vitest";

import { DesiredBufferModel } from "../src/core/delivery/desired-buffer";

describe("DesiredBufferModel", () => {
  it("combines factors, clamps, and adjusts keep-away", () => {
    let jitter = 25;
    const desired = new DesiredBufferModel({ baseMs: 100, minMs: 50, maxMs: 500 });

    desired.setFactor("server", 100);
    desired.setFactor("jitter", () => jitter);
    expect(desired.getDesiredMs()).toBe(225);

    jitter = 50;
    expect(desired.getDesiredMs()).toBe(250);

    desired.penalize();
    expect(desired.getKeepAwayExtraMs()).toBe(100);
    expect(desired.getDesiredMs()).toBe(350);

    desired.penalize(1000);
    expect(desired.getDesiredMs()).toBe(500);

    desired.relax();
    expect(desired.getKeepAwayExtraMs()).toBe(450);

    desired.removeFactor("server");
    desired.reset();
    expect(desired.getDesiredMs()).toBe(150);
  });
});
