// A plan picked on the marketing pricing page (/register?plan=<tier_name>)
// survives email verification in this browser's localStorage, so the first
// sign-in lands on billing with that tier chosen. It is a per-viewer
// convenience: when storage is unavailable the visitor lands on the dashboard.

const STORAGE_KEY = "signup_plan";
const PLAN_PATTERN = /^[a-z0-9_-]{1,32}$/;

type PlanStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

const noStorage: PlanStorage = {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
};

// window.localStorage, or a store that keeps nothing where reading the
// property itself throws (blocked site data).
export function browserPlanStorage(): PlanStorage {
  try {
    return window.localStorage ?? noStorage;
  } catch {
    return noStorage;
  }
}

function validPlan(value: string | null): string | null {
  const plan = value?.trim().toLowerCase() ?? "";
  return PLAN_PATTERN.test(plan) ? plan : null;
}

export function rememberSignupPlan(search: URLSearchParams, storage: PlanStorage): void {
  const plan = validPlan(search.get("plan"));
  if (!plan) return;
  try {
    storage.setItem(STORAGE_KEY, plan);
  } catch {
    // Storage blocked: the plan is not carried past verification.
  }
}

export function storedSignupPlan(storage: PlanStorage): string | null {
  try {
    return validPlan(storage.getItem(STORAGE_KEY));
  } catch {
    return null;
  }
}

export function forgetSignupPlan(storage: PlanStorage): void {
  try {
    storage.removeItem(STORAGE_KEY);
  } catch {
    // Nothing stored or storage blocked.
  }
}

// Sign-in link after verification: with a remembered plan it returns to the
// billing page with that plan in the query.
export function signupPlanLoginHref(
  loginPath: string,
  billingPath: string,
  storage: PlanStorage
): string {
  const plan = storedSignupPlan(storage);
  if (!plan) return loginPath;
  const returnTo = `${billingPath}?${new URLSearchParams({ plan })}`;
  return `${loginPath}?${new URLSearchParams({ return_to: returnTo })}`;
}

// The billing tier a ?plan= value names, or null for an unknown name, the
// tenant's current tier, or Enterprise, which is arranged with sales.
export function requestedPlanTier<
  T extends { id?: string | null; tierName?: string | null; isEnterprise?: boolean | null },
>(tiers: readonly T[], plan: string | null, currentTierId: string | null | undefined): T | null {
  const name = validPlan(plan);
  if (!name) return null;
  const tier = tiers.find((candidate) => (candidate.tierName ?? "").toLowerCase() === name);
  if (!tier || tier.isEnterprise || tier.id === currentTierId) return null;
  return tier;
}
