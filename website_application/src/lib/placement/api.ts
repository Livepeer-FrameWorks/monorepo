import {
  ApplyMediaPlacementChangeStore,
  GetMediaPlacementChangeStore,
  GetMediaPlacementPolicyStore,
  PreviewMediaPlacementStore,
  ReviewMediaPlacementChangeStore,
} from "$houdini";
import type { PlacementAPI } from "./session";

export const placementAPI: PlacementAPI = {
  async policy(scope) {
    const result = await new GetMediaPlacementPolicyStore().fetch({
      variables: { scope },
      policy: "NetworkOnly",
    });
    return result.data?.mediaPlacementPolicy;
  },
  async preview(input) {
    const result = await new PreviewMediaPlacementStore().fetch({
      variables: { input },
      policy: "NetworkOnly",
    });
    return result.data?.previewMediaPlacement;
  },
  async review(input) {
    const result = await new ReviewMediaPlacementChangeStore().fetch({
      variables: { input },
      policy: "NetworkOnly",
    });
    return result.data?.reviewMediaPlacementChange;
  },
  async apply(input) {
    const result = await new ApplyMediaPlacementChangeStore().mutate({ input });
    return result.data?.applyMediaPlacementChange;
  },
  async change(scope, idempotencyKey) {
    const result = await new GetMediaPlacementChangeStore().fetch({
      variables: { scope, idempotencyKey },
      policy: "NetworkOnly",
    });
    return result.data?.mediaPlacementChange;
  },
};
