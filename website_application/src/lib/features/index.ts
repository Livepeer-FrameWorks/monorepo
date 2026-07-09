import registryData from "./registry.json";

export type FeatureStatus = "shipped" | "partial" | "gap" | "roadmap";

export interface SurfaceRequirement {
  required: boolean;
  reason?: string;
  sensitive?: boolean;
  returns_secret_material?: boolean;
  requires_confirmation?: boolean;
  audit_events?: boolean;
  payment_authority?: boolean;
  cost_affecting?: boolean;
  destructive_adjacent?: boolean;
}

export interface Configurability {
  cost_affecting?: boolean;
  security_affecting?: boolean;
  tenant_default?: string;
  per_resource_override?: string;
  effective_value_visible?: string;
  entitlement_bounds?: string;
  audit_events?: string;
  undo?: string;
  dry_run?: string;
}

export type FeatureKind = "product" | "foundation";

export interface FeatureSurfaces {
  graphql: {
    mutations?: string[];
    queries?: string[];
    subscriptions?: string[];
    fields?: string[];
  };
  mcp: { tools?: string[] };
  cli: { commands?: string[] };
  webapp: { routes?: string[] };
  docs: { pages?: string[] };
}

export interface FeatureExample {
  title: string;
  query: string;
}

export interface Feature {
  slug: string;
  name: string;
  // Pillar for top-level families; empty string on subitems (they inherit the parent's pillar).
  area: string;
  kind: FeatureKind;
  description?: string;
  status: FeatureStatus;
  gap_reason?: string;
  required_surfaces: Record<string, SurfaceRequirement>;
  configurability?: Configurability;
  surfaces: FeatureSurfaces;
  examples?: FeatureExample[];
  depends_on?: string[];
  related?: string[];
  aliases?: string[];
  subitems?: Feature[];
  actual_surfaces: Record<string, boolean>;
  // Derived reverse of depends_on, computed by the generator.
  enables?: string[];
  // Own ∪ subitem surfaces; present on families only.
  family_surfaces?: Record<string, boolean>;
}

interface RegistryShape {
  features: Feature[];
}

const registry = registryData as RegistryShape;

/** Top-level families in curated registry order. */
export const features: Feature[] = registry.features;

/** All features — families followed by their subitems — as a flat list. */
export function flattenFeatures(): Feature[] {
  return features.flatMap((f) => [f, ...(f.subitems ?? [])]);
}

export function findFeature(slug: string): Feature | undefined {
  const all = flattenFeatures();
  return all.find((f) => f.slug === slug) ?? all.find((f) => f.aliases?.includes(slug));
}

/** Resolve a list of slugs (e.g. depends_on / enables / related) to features, dropping unknowns. */
export function resolveSlugs(slugs: string[] | undefined): Feature[] {
  if (!slugs?.length) return [];
  const all = flattenFeatures();
  return slugs
    .map((s) => all.find((f) => f.slug === s))
    .filter((f): f is Feature => f !== undefined);
}

/** The family a subitem belongs to, or undefined for top-level slugs. */
export function familyOf(slug: string): Feature | undefined {
  return features.find((f) => (f.subitems ?? []).some((s) => s.slug === slug));
}

export function featuresByArea(): Record<string, Feature[]> {
  const grouped: Record<string, Feature[]> = {};
  for (const f of features) {
    (grouped[f.area] ??= []).push(f);
  }
  return grouped;
}

/** Canonical pillar order and display labels; mirrors scripts/registry/main.go. */
export const PILLAR_ORDER = [
  "streaming",
  "playback",
  "media-library",
  "processing",
  "analytics",
  "infrastructure",
  "commerce",
  "developer",
  "engagement",
  "account",
] as const;

export const PILLAR_LABELS: Record<string, string> = {
  streaming: "Streaming",
  playback: "Playback",
  "media-library": "Media Library",
  processing: "Processing & AI",
  analytics: "Analytics & Observability",
  infrastructure: "Infrastructure & BYOC",
  commerce: "Commerce & Billing",
  developer: "Developer Platform & Agents",
  engagement: "Engagement & Interactivity",
  account: "Account & Apps",
};

export function pillarLabel(area: string): string {
  return PILLAR_LABELS[area] ?? area;
}

const STATUS_ORDER: Record<FeatureStatus, number> = {
  shipped: 0,
  partial: 1,
  gap: 2,
  roadmap: 3,
};

export function statusRank(s: FeatureStatus): number {
  return STATUS_ORDER[s];
}

export const SURFACE_KEYS = ["graphql", "mcp", "cli", "webapp", "docs"] as const;
export type SurfaceKey = (typeof SURFACE_KEYS)[number];

export interface SurfaceCell {
  required: boolean;
  filled: boolean;
  reason?: string;
  sensitive?: boolean;
}

export function surfaceCell(f: Feature, surface: SurfaceKey): SurfaceCell {
  const req = f.required_surfaces[surface];
  return {
    required: req?.required ?? false,
    filled: f.actual_surfaces[surface] ?? false,
    reason: req?.reason,
    sensitive: req?.sensitive,
  };
}
