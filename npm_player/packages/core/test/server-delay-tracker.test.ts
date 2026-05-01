import { describe, expect, it } from "vitest";
import { ServerDelayTracker } from "../src/core/mist/server-delay";

describe("ServerDelayTracker", () => {
  it("tracks moving average", () => {
    let now = 0;
    const t = new ServerDelayTracker(3, () => now);

    t.beginRequest("seek");
    now = 100;
    t.resolveRequest("seek");

    t.beginRequest("set_speed");
    now = 220;
    t.resolveRequest("set_speed");

    expect(t.getAverageDelay()).toBe(110);
  });
});
