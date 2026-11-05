import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";

let paymentError = {
  message: "Bitte prüfe das Rechnungsland.",
  extensions: { code: "CONSUMER_LOCATION_CONFLICT" },
};

vi.mock("$lib/config", () => ({ getDocsSiteUrl: () => "https://docs.example.test" }));
vi.mock("$houdini", () => ({
  GetPrepaidBalanceStore: class {
    async fetch() {
      return {
        data: {
          prepaidBalance: {
            balanceCents: 1000,
            reservedBalanceCents: 0,
            availableBalanceCents: 1000,
            currency: "EUR",
            isLowBalance: false,
          },
        },
      };
    }
  },
  GetBalanceTransactionsStore: class {
    async fetch() {
      return { data: { balanceTransactionsConnection: { nodes: [], totalCount: 0 } } };
    }
  },
  GetBillingStatusStore: class {
    async fetch() {
      return {
        data: {
          billingStatus: { subscription: { billingModel: "prepaid" }, setupProviders: ["stripe"] },
        },
      };
    }
  },
  GetBillingDetailsStore: class {
    async fetch() {
      return { data: { billingDetails: { presentmentCurrency: "EUR" } } };
    }
  },
  CreateCardTopupStore: class {
    async mutate() {
      return { errors: [paymentError] };
    }
  },
  CreateCryptoTopupStore: class {},
  GetCryptoTopupStatusStore: class {},
}));

const { default: BalancePage } = await import("../../src/routes/account/balance/+page.svelte");
afterEach(() => cleanup());

describe("billing payment guidance", () => {
  it.each([
    ["CONSUMER_LOCATION_CONFLICT", "Bitte prüfe das Rechnungsland."],
    ["VAT_NUMBER_REQUIRED", "Please correct the tax identifier."],
    ["VAT_NUMBER_INVALID", "This identifier could not be validated."],
  ])(
    "links to saved billing details for %s independently of message wording",
    async (code, message) => {
      paymentError = { message, extensions: { code } };
      render(BalancePage);
      await fireEvent.click(await screen.findByRole("button", { name: /^Pay / }));
      expect(await screen.findByText(message)).toBeTruthy();
      const link = await screen.findByRole("link", { name: "Complete billing profile" });
      expect(link.getAttribute("href")).toBe("/settings");
    }
  );

  it("keeps supplier setup failures distinct from a customer profile edit", async () => {
    paymentError = {
      message: "Payments await tax setup. Contact support.",
      extensions: { code: "FOREIGN_TAX_POLICY_REQUIRED" },
    };
    render(BalancePage);
    await fireEvent.click(await screen.findByRole("button", { name: /^Pay / }));
    await screen.findByText(paymentError.message);
    expect(screen.queryByRole("link", { name: "Complete billing profile" })).toBeNull();
  });
});
