import {
  ApplyClusterMediaConsentChangeStore,
  GetClusterMediaConsentChangeStore,
  GetClusterMediaConsentStore,
  ReviewClusterMediaConsentChangeStore,
} from "$houdini";
import type { ConsentAPI } from "./consent-session";

export const consentAPI: ConsentAPI = {
  async consent(clusterId) {
    const result = await new GetClusterMediaConsentStore().fetch({
      variables: { clusterId },
      policy: "NetworkOnly",
    });
    return result.data?.clusterMediaConsent;
  },
  async review(input) {
    const result = await new ReviewClusterMediaConsentChangeStore().fetch({
      variables: { input },
      policy: "NetworkOnly",
    });
    return result.data?.reviewClusterMediaConsentChange;
  },
  async apply(input) {
    const result = await new ApplyClusterMediaConsentChangeStore().mutate({ input });
    return result.data?.applyClusterMediaConsentChange;
  },
  async change(clusterId, idempotencyKey) {
    const result = await new GetClusterMediaConsentChangeStore().fetch({
      variables: { clusterId, idempotencyKey },
      policy: "NetworkOnly",
    });
    return result.data?.clusterMediaConsentChange;
  },
};
