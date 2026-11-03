// Samples the /stream-test picker offers: each demo fixture over the player's
// default protocol and over WHEP, and the VOD sample when a playback ID is set.
export function streamTestSamples(fixtures, vodPlaybackId) {
  const live = fixtures.flatMap((fixture) => [
    {
      id: fixture.id,
      label: `FrameWorks ${fixture.label.toLowerCase()} stream`,
      group: "Live",
      source: { mode: "playback", playbackId: fixture.id },
    },
    {
      id: `${fixture.id}-whep`,
      label: `FrameWorks ${fixture.label.toLowerCase()} stream over WHEP`,
      group: "Live",
      source: { mode: "playback", playbackId: fixture.id, protocol: "WHEP" },
    },
  ]);
  const vodId = String(vodPlaybackId ?? "").trim();
  if (!vodId) return live;
  return [
    ...live,
    {
      id: "frameworks-vod",
      label: "FrameWorks video on demand",
      group: "On demand",
      source: { mode: "playback", playbackId: vodId },
    },
  ];
}
