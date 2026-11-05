import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";

let resolveCard: (value: unknown) => void;
let resolveCrypto: (value: unknown) => void;
let cardInput: { input: { amountCents: number } } | null;
let cryptoInput: { input: { amountCents: number } } | null;
const conversion = {
  originalAmountCents: 12100,
  originalCurrency: "EUR",
  eurAmountCents: 12100,
  unitsPerEur: "1",
  referenceDate: "2026-11-05",
};
vi.mock("$lib/config", () => ({ getDocsSiteUrl: () => "https://docs.example.test" }));
vi.mock("$houdini", () => ({
  GetPrepaidBalanceStore: class {
    async fetch() {
      return {
        data: {
          prepaidBalance: {
            balanceCents: 0,
            reservedBalanceCents: 0,
            availableBalanceCents: 0,
            currency: "EUR",
            isLowBalance: true,
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
    async mutate(input: { input: { amountCents: number } }) {
      cardInput = input;
      return await new Promise((resolve) => {
        resolveCard = resolve;
      });
    }
  },
  CreateCryptoTopupStore: class {
    async mutate(input: { input: { amountCents: number } }) {
      cryptoInput = input;
      return await new Promise((resolve) => {
        resolveCrypto = resolve;
      });
    }
  },
  GetCryptoTopupStatusStore: class {
    async mutate() {
      return { data: { cryptoTopupStatus: { status: "pending", confirmations: 0 } } };
    }
  },
}));
const { default: BalancePage } = await import("../../src/routes/account/balance/+page.svelte");
beforeEach(() => {
  cardInput = null;
  cryptoInput = null;
});
afterEach(cleanup);

it("shows EUR100 net service credit and EUR121 gross charge from the accepted card request", async () => {
  render(BalancePage);
  await screen.findByRole("button", { name: /^Pay / });
  await fireEvent.input(screen.getByPlaceholderText("Custom amount"), { target: { value: "100" } });
  await fireEvent.click(screen.getByRole("button", { name: /^Pay / }));
  await fireEvent.input(screen.getByPlaceholderText("Custom amount"), { target: { value: "200" } });
  resolveCard({
    data: {
      createCardTopup: {
        checkoutUrl: "https://checkout.example.test/quote",
        amountCents: 12100,
        currency: "EUR",
        conversion,
      },
    },
  });
  await screen.findByText("Checkout Ready");
  expect(cardInput?.input.amountCents).toBe(10000);
  expect(screen.getByText(/Service credit:/).textContent).toMatch(/€100\.00/);
  expect(screen.getByText(/Charge:/).textContent).toMatch(/€121\.00.*VAT/);
  expect(screen.queryByText(/credits.*€121\.00/)).toBeNull();
});

it("keeps the crypto quote's net credit separate from gross FX and later form edits", async () => {
  render(BalancePage);
  await screen.findByRole("button", { name: /^Pay / });
  await fireEvent.click(screen.getByRole("button", { name: "Crypto" }));
  await fireEvent.input(screen.getByPlaceholderText("Custom amount"), { target: { value: "100" } });
  await fireEvent.click(screen.getByRole("button", { name: "Get Deposit Address" }));
  await fireEvent.input(screen.getByPlaceholderText("Custom amount"), { target: { value: "200" } });
  resolveCrypto({
    data: {
      createCryptoTopup: {
        topupId: "quote",
        depositAddress: "0x123",
        assetSymbol: "USDC",
        expiresAt: "2026-11-05T23:59:59Z",
        expectedAmountToken: "121",
        quotedPriceUsd: "1",
        quoteSource: "fixture",
        network: "base",
        conversion,
      },
    },
  });
  await screen.findByText("Deposit USDC");
  expect(cryptoInput?.input.amountCents).toBe(10000);
  expect(screen.getByText(/Service credit:/).textContent).toMatch(/€100\.00/);
  expect(screen.getByText(/Charge:/).textContent).toMatch(/€121\.00.*VAT/);
});
