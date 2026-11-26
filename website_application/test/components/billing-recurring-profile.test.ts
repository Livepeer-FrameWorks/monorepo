import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";

const fixture = vi.hoisted(() => ({
  provider: "stripe",
  free: false,
  payg: false,
  automaticPrepaidUsage: false,
  enableUsage: vi.fn(),
  keepFree: vi.fn(),
  paymentMethod: "",
  complete: false,
  taxReview: false,
  ready: true,
  status: "active",
  completeProfile: (_complete: boolean) => {},
}));

vi.mock("$app/paths", () => ({ base: "", resolve: (path: string) => path }));
vi.mock("$lib/config", () => ({
  getMarketingSiteUrl: () => "https://example.test",
  getDocsSiteUrl: () => "https://docs.example.test",
}));
vi.mock("$lib/stores/auth", async () => {
  const { readable } = await import("svelte/store");
  return {
    auth: {
      ...readable({
        isAuthenticated: true,
        user: { email: "buyer@example.test", is_verified: true },
      }),
      checkAuth: vi.fn(),
    },
  };
});
vi.mock("$houdini", async () => {
  const { readable, writable } = await import("svelte/store");
  function query(data: () => Record<string, unknown>) {
    const state = writable({ fetching: false, data: data() });
    return {
      ...state,
      async fetch() {
        const result = { data: data() };
        state.set({ fetching: false, ...result });
        return result;
      },
    };
  }
  class EmptyQuery {
    constructor() {
      return query(() => ({}));
    }
  }
  return {
    fragment: (value: unknown) => readable(value),
    GetBillingStatusStore: class {
      constructor() {
        return query(() => ({
          billingStatus: {
            billingStatus: "active",
            currentTier:
              fixture.free || fixture.payg
                ? {
                    id: fixture.free ? "free" : "payg",
                    tierName: fixture.free ? "free" : "payg",
                    displayName: fixture.free ? "Free" : "Pay As You Go",
                    tierLevel: 1,
                    basePrice: 0,
                    currency: "EUR",
                  }
                : null,
            collectionReady: fixture.ready,
            collectionProvider: fixture.provider,
            setupProviders: ["stripe", "mollie"],
            subscription: {
              status: fixture.status,
              automaticPrepaidUsage: fixture.automaticPrepaidUsage,
              billingModel: fixture.payg ? "prepaid" : "postpaid",
              paymentMethod: fixture.paymentMethod,
            },
          },
        }));
      }
    },
    GetBillingDetailsStore: class {
      constructor() {
        const state = query(() => ({
          billingDetails: { isComplete: fixture.complete, taxReviewRequired: fixture.taxReview },
        }));
        fixture.completeProfile = (complete) =>
          state.set({
            fetching: false,
            data: {
              billingDetails: { isComplete: complete, taxReviewRequired: fixture.taxReview },
            },
          });
        return state;
      }
    },
    GetBillingLifecycleStore: EmptyQuery,
    GetBillingTiersStore: class {
      constructor() {
        return query(() => ({
          billingTiers:
            fixture.free || fixture.payg
              ? [
                  {
                    id: "free",
                    tierName: "free",
                    displayName: "Free",
                    tierLevel: 1,
                    basePrice: 0,
                    currency: "EUR",
                  },
                  {
                    id: "payg",
                    tierName: "payg",
                    displayName: "Pay As You Go",
                    pricingRules: [
                      {
                        meter: "delivered_minutes",
                        model: "tiered_graduated",
                        currency: "EUR",
                        unitPrice: "0.00055",
                        includedQuantity: "0",
                        configJson: "{}",
                      },
                      {
                        meter: "storage_gb_seconds_cold",
                        model: "all_usage",
                        currency: "EUR",
                        unitPrice: "0.035",
                        includedQuantity: "0",
                        configJson:
                          '{"rated_quantity_divisor":2628000,"rated_unit":"gibibyte_month"}',
                      },
                    ],
                    tierLevel: 0,
                    basePrice: 0,
                    currency: "EUR",
                  },
                ]
              : [],
        }));
      }
    },
    GetInvoicesStore: EmptyQuery,
    GetPaymentsStore: EmptyQuery,
    GetMollieMandatesStore: EmptyQuery,
    GetPrepaidBalanceStore: EmptyQuery,
    CreatePaymentStore: class {},
    CreateStripeCheckoutStore: class {},
    CreateMollieFirstPaymentStore: class {},
    CreateMollieSubscriptionStore: class {},
    ChangeBillingTierStore: class {},
    PromoteToPaidStore: class {
      mutate = fixture.keepFree;
    },
    EnablePrepaidUsageStore: class {
      mutate = fixture.enableUsage;
    },
    CreateStripeBillingPortalStore: class {},
    BillingTierFieldsStore: class {},
    PaymentMethod: { CARD: "CARD", CRYPTO_USDC: "CRYPTO_USDC", CRYPTO_ETH: "CRYPTO_ETH" },
  };
});

const { default: BillingPage } = await import("../../src/routes/account/billing/+page.svelte");

beforeEach(() => {
  fixture.provider = "stripe";
  fixture.free = false;
  fixture.payg = false;
  fixture.automaticPrepaidUsage = false;
  fixture.keepFree.mockReset();
  fixture.keepFree.mockImplementation(async () => {
    fixture.automaticPrepaidUsage = false;
    return { data: { promoteToPaid: { __typename: "PromoteToPaidPayload" } } };
  });
  fixture.enableUsage.mockReset();
  fixture.enableUsage.mockResolvedValue({
    data: {
      enablePrepaidUsage: {
        activated: true,
        awaitingFunds: false,
        tierName: "payg",
        billingModel: "prepaid",
      },
    },
  });
  fixture.paymentMethod = "";
  fixture.complete = false;
  fixture.taxReview = false;
  fixture.ready = true;
  fixture.status = "active";
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("recurring collection billing profile", () => {
  it.each(["stripe", "mollie"])(
    "shows a VAT review for a complete %s profile until the recorded review clears",
    async (provider) => {
      fixture.provider = provider;
      fixture.complete = true;
      fixture.taxReview = true;
      render(BillingPage);
      await screen.findByText("Check your VAT details");
      expect(screen.queryByText("Activate a Paid Plan")).toBeNull();
      expect(screen.getByRole("link", { name: "Update billing details" })).toBeTruthy();
      fixture.taxReview = false;
      fixture.completeProfile(true);
      await waitFor(() => expect(screen.queryByText("Check your VAT details")).toBeNull());
    }
  );

  it.each(["ended", "cancelled"])(
    "keeps paid-plan setup guidance visible for a %s subscription",
    async (status) => {
      fixture.status = status;
      fixture.ready = false;
      render(BillingPage);
      await screen.findByText("Activate a Paid Plan");
      expect(screen.queryByText("Complete your billing profile")).toBeNull();
      expect(screen.getByText("Billing details required")).toBeTruthy();
    }
  );

  it.each(["stripe", "mollie"])(
    "prompts an active %s customer outside payment setup until their profile is complete",
    async (provider) => {
      fixture.provider = provider;
      render(BillingPage);
      const notice = await screen.findByText("Complete your billing profile");
      expect(notice).toBeTruthy();
      expect(screen.queryByText("Activate a Paid Plan")).toBeNull();
      expect(
        screen.getByRole("link", { name: "Update billing details" }).getAttribute("href")
      ).toBe("/settings");
      fixture.completeProfile(true);
      await waitFor(() => expect(screen.queryByText("Complete your billing profile")).toBeNull());
    }
  );

  it("does not prompt an active provider with complete details", async () => {
    fixture.complete = true;
    render(BillingPage);
    await screen.findByText("Prepaid Balance");
    expect(screen.queryByText("Complete your billing profile")).toBeNull();
  });

  it("does not show a recurring-provider warning for operator collection", async () => {
    fixture.provider = "operator";
    render(BillingPage);
    await screen.findByText("Prepaid Balance");
    expect(screen.queryByText("Complete your billing profile")).toBeNull();
  });

  it("keeps the prompt visible when recurring collection is not ready", async () => {
    fixture.ready = false;
    render(BillingPage);
    await screen.findByText("Complete your billing profile");
    expect(screen.queryByText("Billing details required")).toBeNull();
  });

  it("uses the retained payment method when collection readiness has no provider", async () => {
    fixture.provider = "";
    fixture.paymentMethod = "mollie";
    fixture.ready = false;
    render(BillingPage);
    await screen.findByText("Complete your billing profile");
  });

  it("does not show a recurring warning without a collection provider", async () => {
    fixture.provider = "";
    render(BillingPage);
    await screen.findByText("Prepaid Balance");
    expect(screen.queryByText("Complete your billing profile")).toBeNull();
  });
});

describe("explicit Free funding preference", () => {
  it("explains retained funds and enables PAYG through the funding action", async () => {
    fixture.free = true;
    fixture.complete = true;
    render(BillingPage);
    const action = await screen.findByRole("button", { name: "Use balance for PAYG" });
    expect(screen.getByTestId("funding-preference").textContent).toContain(
      "Your credit is retained"
    );
    expect(screen.getByText("Pay for usage from your balance")).toBeTruthy();
    const prices = screen.getByTestId("payg-usage-prices");
    expect(prices.textContent).toContain("€0.000550 per delivered minute");
    expect(prices.textContent).toContain("€0.035 per GiB-month");
    expect(screen.getByRole("link", { name: "Review all usage prices" }).getAttribute("href")).toBe(
      "https://example.test/pricing"
    );
    await fireEvent.click(action);
    await waitFor(() => expect(fixture.enableUsage).toHaveBeenCalledExactlyOnceWith(null));
  });
  it("explains automatic activation without promising Free allowances under PAYG", async () => {
    fixture.free = true;
    fixture.automaticPrepaidUsage = true;
    fixture.complete = true;
    render(BillingPage);
    expect((await screen.findByTestId("funding-preference")).textContent).toContain(
      "Free allowances stay with the Free phase"
    );
  });
});

it("can retain Free before its first funding without pretending to switch plans", async () => {
  fixture.free = true;
  fixture.automaticPrepaidUsage = true;
  fixture.complete = true;
  render(BillingPage);
  await fireEvent.click(await screen.findByRole("button", { name: "Keep Free limits" }));
  await waitFor(() => expect(fixture.keepFree).toHaveBeenCalledExactlyOnceWith({ tierId: "free" }));
  await waitFor(() =>
    expect(screen.getByTestId("funding-preference").textContent).toContain(
      "Free and its usage limits remain active"
    )
  );
  expect(screen.queryByRole("button", { name: "Keep Free limits" })).toBeNull();
});

it("explains exhausted PAYG fallback without promising to change existing recordings", async () => {
  fixture.payg = true;
  render(BillingPage);
  const policy = await screen.findByTestId("payg-exhaustion-policy");
  expect(policy.textContent).toContain("eligible verified accounts return to Free");
  expect(policy.textContent).toContain("Existing recordings keep their expiry");
  expect(policy.textContent?.replace(/\s+/g, " ")).toContain("unless you explicitly choose Free");
});
