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

// The billing page with the remembered plan in the query, or null when no
// plan is remembered.
export function signupPlanBillingPath(billingPath: string, storage: PlanStorage): string | null {
  const plan = storedSignupPlan(storage);
  if (!plan) return null;
  return `${billingPath}?${new URLSearchParams({ plan })}`;
}

// Sign-in link after verification: with a remembered plan it returns to the
// billing page with that plan in the query.
export function signupPlanLoginHref(
  loginPath: string,
  billingPath: string,
  storage: PlanStorage
): string {
  const returnTo = signupPlanBillingPath(billingPath, storage);
  if (!returnTo) return loginPath;
  return `${loginPath}?${new URLSearchParams({ return_to: returnTo })}`;
}

// Where a successful sign-in goes: the validated return_to, else the billing
// page for a remembered plan (the visitor opened /login without the
// verify-email link), else the dashboard.
export function postLoginTarget(
  returnTo: string | null,
  billingPath: string,
  homePath: string,
  storage: PlanStorage
): string {
  return returnTo ?? signupPlanBillingPath(billingPath, storage) ?? homePath;
}

type PlanTier = { id?: string | null; tierName?: string | null; isEnterprise?: boolean | null };

// The billing tier a ?plan= value names, or null for an unknown name, the
// tenant's current tier, or Enterprise, which is arranged with sales.
export function requestedPlanTier<T extends PlanTier>(
  tiers: readonly T[],
  plan: string | null,
  currentTierId: string | null | undefined
): T | null {
  const name = validPlan(plan);
  if (!name) return null;
  const tier = tiers.find((candidate) => (candidate.tierName ?? "").toLowerCase() === name);
  if (!tier || tier.isEnterprise || tier.id === currentTierId) return null;
  return tier;
}

// What the billing page does with ?plan= on load. With no plan in the URL, or
// before the tier list has loaded, nothing is consumed (cleanedHref is null),
// so the plan is not forgotten unapplied. Otherwise the page drops ?plan= from
// the URL (cleanedHref, a path with search and hash), forgets the stored plan
// and opens the change flow for tier when it is not null.
export function resolveRequestedPlan<T extends PlanTier>(
  href: string,
  tiers: readonly T[],
  currentTierId: string | null | undefined
): { tier: T | null; cleanedHref: string | null } {
  const url = new URL(href);
  const plan = url.searchParams.get("plan");
  if (!plan || tiers.length === 0) return { tier: null, cleanedHref: null };
  url.searchParams.delete("plan");
  return {
    tier: requestedPlanTier(tiers, plan, currentTierId),
    cleanedHref: `${url.pathname}${url.search}${url.hash}`,
  };
}
