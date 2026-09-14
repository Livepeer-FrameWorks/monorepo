import type {
  ApplyMediaPlacementChange$input,
  ApplyMediaPlacementChange$result,
  GetMediaPlacementPolicy$result,
  PreviewMediaPlacement$input,
  PreviewMediaPlacement$result,
  ReviewMediaPlacementChange$input,
  ReviewMediaPlacementChange$result,
} from "$houdini";

type Unmasked<T> = T extends (infer Item)[]
  ? Unmasked<Item>[]
  : T extends object
    ? { [Key in keyof T as Key extends " $fragments" ? never : Key]: Unmasked<T[Key]> }
    : T;

export type PolicyResult = Unmasked<GetMediaPlacementPolicy$result["mediaPlacementPolicy"]>;
export type Policy = Extract<PolicyResult, { __typename: "MediaPlacementPolicyState" }>;
export type ReviewResult = Unmasked<
  ReviewMediaPlacementChange$result["reviewMediaPlacementChange"]
>;
export type Review = Extract<ReviewResult, { __typename: "MediaPlacementReview" }>;
export type PreviewResult = Unmasked<PreviewMediaPlacement$result["previewMediaPlacement"]>;
export type Preview = Extract<PreviewResult, { __typename: "MediaPlacementPreview" }>;
export type ChangeResult = Unmasked<ApplyMediaPlacementChange$result["applyMediaPlacementChange"]>;
export type Change = Extract<ChangeResult, { __typename: "MediaPlacementChange" }>;
export type ApplyInput = ApplyMediaPlacementChange$input["input"];
export type ReviewInput = ReviewMediaPlacementChange$input["input"];
export type PreviewInput = PreviewMediaPlacement$input["input"];
export type Scope = ApplyInput["scope"];
export type Verb = ApplyInput["updates"][number]["verb"];
export type Rules = NonNullable<ApplyInput["updates"][number]["rules"]>;
export type Selector = NonNullable<Rules["constraints"]["deny"]>[number];
export type Group = NonNullable<Rules["preferences"]>["groups"][number];
export type Drafts = Record<Verb, Rules | null>;
export type Rollout = Policy["rollout"];

type SavedRules = NonNullable<Policy["verbs"][number]["ownRules"]>;

export function copySelector(selector: Selector): Selector {
  return {
    clusterIds: [...(selector.clusterIds ?? [])],
    ownerIds: [...(selector.ownerIds ?? [])],
    regions: [...(selector.regions ?? [])],
    classes: [...(selector.classes ?? [])],
    charging: [...(selector.charging ?? [])],
  };
}

// Pick wire fields explicitly; Houdini fragment metadata is not mutation input.
export function copyRules(rules: SavedRules | Rules | null | undefined): Rules | null {
  if (!rules) return null;
  return {
    schemaVersion: rules.schemaVersion ?? 1,
    constraints: {
      allow: rules.constraints.allow
        ? { any: rules.constraints.allow.any.map(copySelector) }
        : null,
      deny: (rules.constraints.deny ?? []).map(copySelector),
    },
    preferences: rules.preferences
      ? {
          groups: rules.preferences.groups.map((group) => ({
            id: group.id,
            match: copySelector(group.match),
            order: group.order ?? "DISTANCE",
            spillover: group.spillover ?? "NEVER",
            maxDistanceKm: group.maxDistanceKm ?? 0,
            geoHoleDistanceKm: group.geoHoleDistanceKm ?? 0,
            minImprovementKm: group.minImprovementKm ?? 0,
            priceCurrency: group.priceCurrency ?? null,
            priceUnit: group.priceUnit ?? null,
          })),
        }
      : null,
  };
}

export function savedDrafts(policy: Policy): Drafts {
  return {
    INGEST: copyRules(policy.verbs.find((item) => item.verb === "INGEST")?.ownRules),
    SERVE: copyRules(policy.verbs.find((item) => item.verb === "SERVE")?.ownRules),
  };
}

export function updatesFor(policy: Policy, drafts: Drafts): ApplyInput["updates"] {
  const saved = savedDrafts(policy);
  return (["INGEST", "SERVE"] as const).flatMap((verb) => {
    const rules = copyRules(drafts[verb]);
    if (JSON.stringify(rules) === JSON.stringify(saved[verb])) return [];
    return [{ verb, kind: rules ? ("SET" as const) : ("CLEAR" as const), rules }];
  });
}

export function newGroup(id: string, match: Selector = {}): Group {
  return {
    id,
    match: copySelector(match),
    order: "DISTANCE",
    spillover: "NEVER",
    maxDistanceKm: 0,
    geoHoleDistanceKm: 0,
    minImprovementKm: 0,
  };
}

export const presetLabels: Record<string, string> = {
  closest_available: "Closest available",
  my_clusters_first: "My clusters first",
  my_clusters_only: "My clusters only",
  no_official: "No official capacity",
};

export const presetDescriptions: Record<string, string> = {
  closest_available: "Use any eligible capacity and choose the closest healthy destination.",
  my_clusters_first:
    "Prefer clusters you operate, then use other connected capacity only when yours is full.",
  my_clusters_only: "Never place this work outside clusters you operate.",
  no_official: "Use eligible self-hosted or marketplace capacity, but never official clusters.",
};

export function presetRules(preset: string): Rules {
  const own = newGroup("my-clusters", { classes: ["TENANT_PRIVATE"] });
  const rules: Rules = { schemaVersion: 1, constraints: { deny: [] }, preferences: null };
  switch (preset) {
    case "closest_available":
      rules.preferences = { groups: [newGroup("closest")] };
      break;
    case "my_clusters_first":
      own.spillover = "CAPACITY_ONLY";
      rules.preferences = { groups: [own, newGroup("connected-fallback")] };
      break;
    case "my_clusters_only":
      rules.constraints.allow = { any: [copySelector(own.match)] };
      rules.preferences = { groups: [own] };
      break;
    case "no_official":
      rules.constraints.deny = [copySelector({ classes: ["PLATFORM_OFFICIAL"] })];
      rules.preferences = { groups: [newGroup("closest")] };
      break;
    default:
      throw new Error("This preset is not supported by the editor.");
  }
  return copyRules(rules)!;
}

export function matchingPreset(rules: Rules | null, supported: string[]): string | null {
  if (!rules) return null;
  const serialized = JSON.stringify(copyRules(rules));
  return (
    supported.find(
      (preset) =>
        preset in presetLabels && serialized === JSON.stringify(copyRules(presetRules(preset)))
    ) ?? null
  );
}

export function selectorLabel(selector: Selector): string {
  const classes = {
    PLATFORM_OFFICIAL: "official clusters",
    TENANT_PRIVATE: "my clusters",
    THIRD_PARTY_MARKETPLACE: "connected marketplace clusters",
  };
  const parts = [
    selector.classes?.map((value) => classes[value]).join(" or "),
    selector.clusterIds?.length ? `${selector.clusterIds.length} selected cluster(s)` : "",
    selector.ownerIds?.length ? `${selector.ownerIds.length} selected operator(s)` : "",
    selector.regions?.length ? `regions: ${selector.regions.join(", ")}` : "",
    selector.charging
      ?.map((value) => (value === "RATED" ? "rated" : "permanently free"))
      .join(" or "),
  ].filter(Boolean);
  return parts.length ? parts.join(" · AND · ") : "All connected, entitled capacity";
}

export function rolloutLabel(status: Rollout["status"]): string {
  return {
    NOT_CONFIGURED: "No custom rules at this scope",
    PENDING: "Saved · waiting for enforcement",
    EFFECTIVE: "Effective for new decisions",
    BLOCKED: "Saved · deployment blocked",
    SUPERSEDED: "Replaced by a newer revision",
  }[status];
}

export function rolloutNeedsRefresh(policy: Policy): boolean {
  return (
    policy.rollout.status === "PENDING" ||
    policy.rollout.status === "BLOCKED" ||
    (policy.rollout.status === "NOT_CONFIGURED" &&
      policy.scope.kind === "STREAM" &&
      policy.parentRevision !== "0" &&
      policy.activeParentRevision !== policy.parentRevision)
  );
}

export function validateDraft(rules: Rules | null): string[] {
  if (!rules) return [];
  const errors: string[] = [];
  const ids = new Set<string>();
  for (const [index, group] of (rules.preferences?.groups ?? []).entries()) {
    const label = `Group ${index + 1}`;
    if (!group.id.trim() || ids.has(group.id))
      errors.push(`${label}: group identity must be unique.`);
    ids.add(group.id);
    for (const key of ["maxDistanceKm", "geoHoleDistanceKm", "minImprovementKm"] as const) {
      const value = group[key] ?? 0;
      if (!Number.isFinite(value) || value < 0)
        errors.push(`${label}: distances must be finite and non-negative.`);
    }
    if (
      (group.spillover === "GEO_HOLE" || group.spillover === "CAPACITY_OR_GEO_HOLE") &&
      !(Number(group.geoHoleDistanceKm) > 0)
    ) {
      errors.push(`${label}: geographic fallback needs a positive geographic limit.`);
    }
    if (group.order === "PRICE" && (!group.priceCurrency?.trim() || !group.priceUnit?.trim())) {
      errors.push(`${label}: price ordering needs a currency and comparable unit.`);
    }
  }
  return errors;
}

export function errorMessage(
  result: PolicyResult | ReviewResult | PreviewResult | ChangeResult | undefined
): string {
  if (!result) return "No confirmed response. Try again or check the change status.";
  if ("message" in result) {
    const fields =
      "fields" in result
        ? result.fields.map((field) => `${field.groupId ?? field.path}: ${field.message}`)
        : [];
    // The service returns the retry delay and the revision that overtook this
    // one. Both are actionable, and the generic prose alone leaves the operator
    // guessing how long to wait or what they are now behind.
    const specifics: string[] = [];
    const retryAfter = "retryAfterSeconds" in result ? result.retryAfterSeconds : null;
    if (typeof retryAfter === "number" && retryAfter > 0) {
      specifics.push(`Retry in ${retryAfter}s.`);
    }
    const currentRevision = "currentRevision" in result ? result.currentRevision : null;
    if (currentRevision) {
      specifics.push(`Current revision is ${currentRevision}.`);
    }
    return [result.message, ...fields, ...specifics].join(" ");
  }
  return "The service returned an unexpected response.";
}
