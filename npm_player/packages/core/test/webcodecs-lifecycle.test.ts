import { describe, expect, it } from "vitest";

import { WebCodecsPlayerImpl } from "../src/players/WebCodecsPlayer";

describe("WebCodecs lifecycle", () => {
  it("ignores stale play and pause calls after teardown", async () => {
    const player = new WebCodecsPlayerImpl();
    await player.destroy();

    await expect(player.play()).resolves.toBeUndefined();
    expect(() => player.pause()).not.toThrow();
  });
});
