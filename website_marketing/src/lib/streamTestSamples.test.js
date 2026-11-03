import assert from "node:assert/strict";
import { test } from "node:test";

import { streamTestSamples } from "./streamTestSamples.js";

const fixtures = [{ id: "frameworks-demo", label: "Demo" }];

test("live fixtures are offered over the default protocol and over WHEP", () => {
  assert.deepEqual(
    streamTestSamples(fixtures).map((sample) => [sample.id, sample.source]),
    [
      ["frameworks-demo", { mode: "playback", playbackId: "frameworks-demo" }],
      [
        "frameworks-demo-whep",
        { mode: "playback", playbackId: "frameworks-demo", protocol: "WHEP" },
      ],
    ]
  );
});
