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

export const abrLine = "Adaptive bitrate included";
