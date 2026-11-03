import { describe, expect, it } from "vitest";
import {
  forgetSignupPlan,
  rememberSignupPlan,
  requestedPlanTier,
  signupPlanLoginHref,
  storedSignupPlan,
} from "./signupPlan";

function memoryStorage() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
  };
}

const blockedStorage = {
  getItem: () => {
    throw new Error("blocked");
  },
  setItem: () => {
    throw new Error("blocked");
  },
  removeItem: () => {
    throw new Error("blocked");
  },
};

describe("signup plan", () => {
  it("carries a pricing-page plan to the billing page through sign-in", () => {
    const storage = memoryStorage();
    rememberSignupPlan(
      new URLSearchParams("utm_source=marketing&utm_content=pricing-developer&plan=developer"),
      storage
    );
    expect(storedSignupPlan(storage)).toBe("developer");

    const href = signupPlanLoginHref("/login", "/account/billing", storage);
    const url = new URL(href, "https://app.example");
    expect(url.pathname).toBe("/login");
    expect(url.searchParams.get("return_to")).toBe("/account/billing?plan=developer");

    forgetSignupPlan(storage);
    expect(signupPlanLoginHref("/login", "/account/billing", storage)).toBe("/login");
  });

  it("ignores a missing or malformed plan", () => {
    const storage = memoryStorage();
    rememberSignupPlan(new URLSearchParams("utm_source=marketing"), storage);
    rememberSignupPlan(new URLSearchParams("plan=%2F%2Fevil.example"), storage);
    expect(storedSignupPlan(storage)).toBeNull();
  });

  it("falls back to the plain sign-in link when storage is blocked", () => {
    rememberSignupPlan(new URLSearchParams("plan=supporter"), blockedStorage);
    expect(storedSignupPlan(blockedStorage)).toBeNull();
    expect(signupPlanLoginHref("/login", "/account/billing", blockedStorage)).toBe("/login");
    expect(() => forgetSignupPlan(blockedStorage)).not.toThrow();
  });

  it("resolves the plan to a tier other than the current one or Enterprise", () => {
    const tiers = [
      { id: "t-free", tierName: "free" },
      { id: "t-dev", tierName: "developer" },
      { id: "t-ent", tierName: "enterprise", isEnterprise: true },
    ];
    expect(requestedPlanTier(tiers, "Developer", "t-free")?.id).toBe("t-dev");
    expect(requestedPlanTier(tiers, "developer", "t-dev")).toBeNull();
    expect(requestedPlanTier(tiers, "enterprise", "t-free")).toBeNull();
    expect(requestedPlanTier(tiers, "unknown", "t-free")).toBeNull();
    expect(requestedPlanTier(tiers, null, "t-free")).toBeNull();
  });
});
