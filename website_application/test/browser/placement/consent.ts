import type { Consent, ConsentAPI, ConsentChange } from "$lib/placement/consent-session";
let saved: Consent = {
  __typename: "MediaCapacityConsent",
  clusterId: "fixture-eu",
  revision: "12",
  allowIngest: true,
  allowServe: true,
  allowExternalSource: true,
  canManage: true,
  rollout: {
    status: "PENDING",
    requiredRecipients: 0,
    appliedRecipients: 0,
    pendingRecipients: [],
    existingSessionsRetained: true,
    updatedAt: null,
  },
};
const changes = new Map<string, ConsentChange>();
export const consentAPI: ConsentAPI = {
  async consent() {
    return structuredClone(saved);
  },
  async review(input) {
    return {
      __typename: "MediaPlacementReview",
      reviewToken: "fixture-review",
      digest: "fixture-digest",
      expiresAt: new Date(Date.now() + 60000).toISOString(),
      differences: ["allowIngest", "allowServe", "allowExternalSource"].flatMap((key) => {
        const field = key as "allowIngest" | "allowServe" | "allowExternalSource";
        return input[field] === saved[field]
          ? []
          : [
              {
                path: key,
                label: key,
                before: saved[field] ? "Allowed" : "Denied",
                after: input[field] ? "Allowed" : "Denied",
              },
            ];
      }),
      warnings: [
        {
          id: "fixture-warning",
          severity: "WARNING",
          message: "Fixture warning: new placements may lose this destination.",
          acknowledgementRequired: true,
        },
      ],
      impact: {
        affectedStreams: 2,
        activePublishers: 1,
        complete: false,
        existingSessionsRetained: true,
      },
    };
  },
  async apply(input) {
    const result: ConsentChange = {
      __typename: "MediaCapacityConsentChange",
      clusterId: input.clusterId,
      revision: String(BigInt(saved.revision) + 1n),
      idempotencyKey: input.idempotencyKey,
      digest: "fixture-digest",
      createdAt: new Date().toISOString(),
      rollout: saved.rollout,
    };
    changes.set(input.idempotencyKey, result);
    saved = {
      ...saved,
      allowIngest: input.allowIngest,
      allowServe: input.allowServe,
      allowExternalSource: input.allowExternalSource,
      revision: result.revision,
    };
    return structuredClone(result);
  },
  async change(_clusterId, key) {
    return changes.get(key);
  },
};
