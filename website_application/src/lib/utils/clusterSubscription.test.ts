import { describe, expect, it } from "vitest";
import { clusterSubscriptionOutcome } from "./clusterSubscription";

describe("clusterSubscriptionOutcome", () => {
  it("returns an HTTPS checkout URL from the pending payment status", () => {
    expect(
      clusterSubscriptionOutcome({
        errors: [
          { message: "status:pending_payment checkout_url:https://checkout.example/session" },
        ],
      })
    ).toEqual({
      status: "pending_payment",
      checkoutUrl: "https://checkout.example/session",
    });
  });

  it("rejects malformed or non-HTTPS checkout destinations", () => {
    expect(
      clusterSubscriptionOutcome({
        errors: [{ message: "status:pending_payment checkout_url:javascript:alert(1)" }],
      })
    ).toEqual({ status: "error", message: "Payment checkout could not be opened." });
  });

  it("distinguishes active, approval, and backend errors", () => {
    expect(clusterSubscriptionOutcome({ data: { subscribeToCluster: true } })).toEqual({
      status: "active",
    });
    expect(
      clusterSubscriptionOutcome({ errors: [{ message: "status:pending_approval" }] })
    ).toEqual({ status: "pending_approval" });
    expect(clusterSubscriptionOutcome({ errors: [{ message: "billing unavailable" }] })).toEqual({
      status: "error",
      message: "billing unavailable",
    });
  });
});
