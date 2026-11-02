// Readers for src/data/status.json, which make generate-feature-registry
// writes from docs/platform-features.yaml. Pages pass the imported document in
// so these stay plain functions the node tests can call.

const trimSlash = (url) => String(url ?? "").replace(/\/+$/, "");

export const tierAnchor = (label) =>
  String(label)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");

// A missing id is a build error: the page would otherwise render a status
// nobody declared.
export function featureOf(status, id) {
  const feature = status.features[id];
  if (!feature) {
    throw new Error(`status.json has no feature "${id}"`);
  }
  return feature;
}

export function tierOf(status, horizon) {
  const tier = status.tiers.find((entry) => entry.horizon === horizon);
  if (!tier) {
    throw new Error(`status.json has no tier for horizon "${horizon}"`);
  }
  return tier;
}

// Badge tone per status: shipping features read green, beta cyan, the next
// horizon yellow, and the later horizon neutral.
export function statusTone(feature) {
  if (feature.horizon === "now") return feature.maturity ? "cyan" : "green";
  if (feature.horizon === "next") return "yellow";
  return "neutral";
}

// Short label for compact lists such as the landing-page journey: shipped work
// reads Available or Expanding (Beta for beta), planned work reads its tier tag.
export function compactLabel(feature) {
  if (feature.horizon !== "now") return feature.tag;
  return feature.maturity ? feature.tag : feature.availability;
}

// Styling key for a status chip: available, expanding, or beta for shipped
// work, otherwise the horizon (next or later).
export function statusKey(feature) {
  if (feature.horizon !== "now") return feature.horizon;
  if (feature.maturity) return "beta";
  return feature.status === "shipped" ? "available" : "expanding";
}

// Showcase groups in display order, each with its anchor and features.
export function showcaseTiers(status) {
  return status.tiers.map((tier) => ({
    ...tier,
    anchor: tierAnchor(tier.label),
    features: (status.showcase[tier.horizon] ?? []).map((id) => featureOf(status, id)),
  }));
}

// Resolves a showcase link to a marketing route (`to`) or a docs URL (`href`).
export function showcaseLink(link, docsUrl) {
  if (!link) return null;
  if (link.site === "docs") {
    return { label: link.label, href: `${trimSlash(docsUrl)}${link.path}` };
  }
  return { label: link.label, to: link.path };
}

export function roadmapHref(feature, docsUrl) {
  return feature.roadmap ? `${trimSlash(docsUrl)}${feature.roadmap.path}` : null;
}

// Path to a feature's row on the showcase page.
export const showcaseHref = (id) => `/showcase#${id}`;
