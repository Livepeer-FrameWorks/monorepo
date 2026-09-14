import type { PlacementAPI } from "$lib/placement/session";
import type { Policy, Scope } from "$lib/placement/model";

function resultScope(scope: Scope): Policy["scope"] {
  return {
    kind: scope.kind,
    streamId: typeof scope.streamId === "string" ? scope.streamId : null,
  };
}

function policy(scope: Scope): Policy {
  return {
    __typename: "MediaPlacementPolicyState",
    scope: resultScope(scope),
    revision: "12",
    parentRevision: "0",
    activeRevision: "11",
    activeParentRevision: null,
    verbs: (["INGEST", "SERVE"] as const).map((verb) => ({
      verb,
      ownRules: null,
      inheritedRules: null,
      requestedEffective: { schemaVersion: 1, digest: "fixture", layers: [], groups: [] },
    })),
    rollout: {
      status: "PENDING",
      requiredRecipients: 2,
      appliedRecipients: 1,
      pendingRecipients: [
        {
          id: "fixture-us",
          name: "US control cell (fixture)",
          status: "PENDING",
          reason: "Waiting for signed authority acknowledgement",
          authorityExpiresAt: null,
        },
      ],
      existingSessionsRetained: true,
      updatedAt: null,
    },
    actions: {
      canRead: true,
      canManage: true,
      canPreview: true,
      canInspectPrivateCandidates: false,
    },
    features: {
      schemaVersion: 1,
      geographicSpillover: true,
      priceOrdering: false,
      supportedPresets: [
        "closest_available",
        "my_clusters_first",
        "my_clusters_only",
        "no_official",
      ],
    },
  };
}

export const placementAPI: PlacementAPI = {
  async policy(scope) {
    return policy(scope);
  },
  async preview(input) {
    return {
      __typename: "MediaPlacementPreview",
      scope: resultScope(input.scope),
      verb: input.verb,
      revision: "12",
      parentRevision: "0",
      digest: "fixture-preview",
      reason: "Closest eligible capacity for this fixture request",
      selected: {
        clusterId: "fixture-eu",
        clusterName: "Fixture: my EU cluster",
        region: "eu-west",
        nodeId: null,
        groupId: "default",
        reason: "Selected by policy and distance",
        distanceKm: input.coordinates ? 18 : null,
        requiresSourcePull: input.verb === "SERVE",
        price: null,
      },
      candidates: [],
      transitions: [],
      observedAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      complete: true,
      sourceEvaluated: false,
      activeIngestClusterId: null,
    };
  },
  async review(input) {
    return {
      __typename: "MediaPlacementReview",
      reviewToken: "fixture-review",
      digest: "fixture-review",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      differences: input.updates.map((update) => ({
        path: update.verb.toLowerCase(),
        label: update.verb === "SERVE" ? "Viewer routing" : "Publishing routing",
        before: "Platform default",
        after: "Custom approach",
      })),
      warnings: [],
      impact: {
        affectedStreams: 4,
        activePublishers: 1,
        complete: true,
        existingSessionsRetained: true,
      },
    };
  },
  async apply() {
    return undefined;
  },
  async change() {
    return undefined;
  },
};
