type MutationResult = {
  data?: { subscribeToCluster?: boolean | null } | null;
  errors?: readonly { message: string }[] | null;
};

export type ClusterSubscriptionOutcome =
  | { status: "active" }
  | { status: "pending_payment"; checkoutUrl: string }
  | { status: "pending_approval" }
  | { status: "error"; message: string };

export function clusterSubscriptionOutcome(result: MutationResult): ClusterSubscriptionOutcome {
  const message = result.errors?.[0]?.message ?? "";
  if (message.includes("status:pending_payment")) {
    const checkoutUrl = message.match(/checkout_url:(\S+)/)?.[1] ?? "";
    try {
      const parsed = new URL(checkoutUrl);
      if (parsed.protocol === "https:") {
        return { status: "pending_payment", checkoutUrl: parsed.toString() };
      }
    } catch {
      // The mutation failed safely; surface a useful error instead of
      // navigating to an untrusted or malformed URL.
    }
    return { status: "error", message: "Payment checkout could not be opened." };
  }
  if (message.includes("status:pending_approval")) {
    return { status: "pending_approval" };
  }
  if (message) {
    return { status: "error", message };
  }
  if (result.data?.subscribeToCluster) {
    return { status: "active" };
  }
  return { status: "error", message: "Cluster subscription was not activated." };
}
