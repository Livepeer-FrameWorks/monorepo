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

export interface FeatureSurfaces {
  graphql: {
    mutations?: string[];
    queries?: string[];
    subscriptions?: string[];
    fields?: string[];
  };
  mcp: { tools?: string[] };
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
  area: string;
  description?: string;
  status: FeatureStatus;
  gap_reason?: string;
  required_surfaces: Record<string, SurfaceRequirement>;
  configurability?: Configurability;
  surfaces: FeatureSurfaces;
  examples?: FeatureExample[];
  actual_surfaces: Record<string, boolean>;
}

interface RegistryShape {
  features: Feature[];
}

const registry = registryData as RegistryShape;

export const features: Feature[] = registry.features;

export function findFeature(slug: string): Feature | undefined {
  return features.find((f) => f.slug === slug);
}

export function featuresByArea(): Record<string, Feature[]> {
  const grouped: Record<string, Feature[]> = {};
  for (const f of features) {
    (grouped[f.area] ??= []).push(f);
  }
  return grouped;
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

export const SURFACE_KEYS = ["graphql", "mcp", "webapp", "docs"] as const;
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
