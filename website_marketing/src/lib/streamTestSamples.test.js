import assert from "node:assert/strict";
import { test } from "node:test";

import { streamTestSamples } from "./streamTestSamples.js";

const fixtures = [{ id: "frameworks-demo", label: "Demo" }];

test("live fixtures are offered over the default protocol and over WHEP", () => {
  const samples = streamTestSamples(fixtures, "");
  assert.deepEqual(
    samples.map((sample) => [sample.id, sample.group, sample.source.protocol]),
    [
      ["frameworks-demo", "Live", undefined],
      ["frameworks-demo-whep", "Live", "WHEP"],
    ]
  );
});

test("the on-demand sample appears only when a VOD playback ID is set", () => {
  for (const unset of ["", "  ", undefined, null]) {
    assert.equal(
      streamTestSamples(fixtures, unset).some((sample) => sample.group === "On demand"),
      false
    );
  }
  const vod = streamTestSamples(fixtures, " vod-123 ").at(-1);
  assert.equal(vod.group, "On demand");
  assert.deepEqual(vod.source, { mode: "playback", playbackId: "vod-123" });
});
