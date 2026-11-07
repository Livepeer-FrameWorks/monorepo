import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/svelte";

const fixture = vi.hoisted(() => ({
  provider: "stripe",
  paymentMethod: "",
  complete: false,
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
            collectionReady: fixture.ready,
            collectionProvider: fixture.provider,
            setupProviders: ["stripe", "mollie"],
            subscription: {
              status: fixture.status,
              billingModel: "postpaid",
              paymentMethod: fixture.paymentMethod,
            },
          },
        }));
      }
    },
    GetBillingDetailsStore: class {
      constructor() {
        const state = query(() => ({ billingDetails: { isComplete: fixture.complete } }));
        fixture.completeProfile = (complete) =>
          state.set({ fetching: false, data: { billingDetails: { isComplete: complete } } });
        return state;
      }
    },
    GetBillingLifecycleStore: EmptyQuery,
    GetBillingTiersStore: EmptyQuery,
    GetInvoicesStore: EmptyQuery,
    GetPaymentsStore: EmptyQuery,
    GetMollieMandatesStore: EmptyQuery,
    GetPrepaidBalanceStore: EmptyQuery,
    CreatePaymentStore: class {},
    CreateStripeCheckoutStore: class {},
    CreateMollieFirstPaymentStore: class {},
    CreateMollieSubscriptionStore: class {},
    ChangeBillingTierStore: class {},
    PromoteToPaidStore: class {},
    CreateStripeBillingPortalStore: class {},
    BillingTierFieldsStore: class {},
    PaymentMethod: { CARD: "CARD", CRYPTO_USDC: "CRYPTO_USDC", CRYPTO_ETH: "CRYPTO_ETH" },
  };
});

const { default: BillingPage } = await import("../../src/routes/account/billing/+page.svelte");

beforeEach(() => {
  fixture.provider = "stripe";
  fixture.paymentMethod = "";
  fixture.complete = false;
  fixture.ready = true;
  fixture.status = "active";
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("recurring collection billing profile", () => {
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
