// Plan-card wording shared by the landing and pricing pages. Conditions are
// derived from pricing-catalog.json so a tier without a viewer cap never
// carries the viewer-slot caveat.

export const viewerCap = (tier) => tier?.limits?.maxConcurrentViewers ?? 0;

// One line describing multistreaming on a plan card for the given catalog tier.
export function multistreamLine(tier) {
  const viewers = viewerCap(tier);
  if (viewers > 0) {
    return `Multistreaming with no destination cap: each destination uses one of the ${viewers.toLocaleString("en-US")} viewer slots and counts toward delivered minutes`;
  }
  return "Multistreaming with no destination cap; each destination counts toward delivered minutes";
}

export const videoLine = (tier) =>
  `Up to ${(tier?.maxVideoHeight ?? 1080) >= 2160 ? "4K" : `${tier?.maxVideoHeight ?? 1080}p`} video`;

export const abrLine = "Adaptive bitrate included";

const formatHours = (seconds) => {
  const hours = seconds / 3600;
  return `${hours} ${hours === 1 ? "hour" : "hours"}`;
};

// Plan-card lines for an uncapped plan: the absence of stream/viewer caps and
// the catalog's maximum live rewind window. A capped plan (Free) lists its
// caps on its own card instead.
export function capacityLines(tier) {
  const limits = tier?.limits ?? {};
  if ((limits.maxConcurrentStreams ?? 0) > 0 || viewerCap(tier) > 0) return [];
  const lines = ["No cap on concurrent streams or viewers"];
  if ((limits.dvrMaxWindowSeconds ?? 0) > 0) {
    lines.push(`Live rewind (DVR) up to ${formatHours(limits.dvrMaxWindowSeconds)}`);
  }
  return lines;
}
