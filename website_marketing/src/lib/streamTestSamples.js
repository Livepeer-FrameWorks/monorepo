// Samples the /stream-test picker offers: each demo fixture over the player's
// default protocol and over WHEP.
export function streamTestSamples(fixtures) {
  return fixtures.flatMap((fixture) => [
    {
      id: fixture.id,
      label: `FrameWorks ${fixture.label.toLowerCase()} stream`,
      source: { mode: "playback", playbackId: fixture.id },
    },
    {
      id: `${fixture.id}-whep`,
      label: `FrameWorks ${fixture.label.toLowerCase()} stream over WHEP`,
      source: { mode: "playback", playbackId: fixture.id, protocol: "WHEP" },
    },
  ]);
}
