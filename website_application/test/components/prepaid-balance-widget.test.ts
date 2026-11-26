import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, act } from "@testing-library/svelte";

type Grace = {
  active: boolean;
  endsAt: string | null;
  remainingCreditCents: number;
  mediaRecoveryUntil: string | null;
};
let balance: {
  balanceCents: number;
  reservedBalanceCents: number;
  availableBalanceCents: number;
  paymentGrace?: Grace | null;
} = { balanceCents: 0, reservedBalanceCents: 0, availableBalanceCents: 0 };

vi.mock("$houdini", () => ({
  GetPrepaidBalanceStore: class {
    async fetch() {
      return { data: { prepaidBalance: { ...balance, currency: "EUR", isLowBalance: true } } };
    }
  },
}));

const { default: PrepaidBalanceWidget } =
  await import("$lib/components/PrepaidBalanceWidget.svelte");

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function withAvailable(cents: number) {
  balance = { balanceCents: cents, reservedBalanceCents: 0, availableBalanceCents: cents };
}

describe("prepaid balance widget", () => {
  it("explains restrictions at zero when no grace is available", async () => {
    withAvailable(0);
    render(PrepaidBalanceWidget, { billingModel: "prepaid" });
    const notice = await screen.findByText(/Prepaid balance exhausted/);
    expect(notice.textContent).toContain("Free limits");
    expect(notice.textContent).toContain("unpaid usage is retained");
  });

  it("states the suspension when Purser suspended the account", async () => {
    withAvailable(-1500);
    render(PrepaidBalanceWidget, { billingModel: "prepaid", suspended: true });
    expect(await screen.findByText(/Paid service is restricted/)).toBeTruthy();
  });

  it("does not call an account suspended on a reservation alone", async () => {
    // Settled +€5 with €20 reserved: available is -€15, but Purser suspends
    // on the settled balance, so the account is only refusing new sessions.
    balance = { balanceCents: 500, reservedBalanceCents: 2000, availableBalanceCents: -1500 };
    render(PrepaidBalanceWidget, { billingModel: "prepaid", suspended: false });
    expect(await screen.findByText(/Prepaid balance exhausted/)).toBeTruthy();
    expect(screen.queryByText(/suspended/)).toBeNull();
  });

  it("shows no spent-balance rule for a postpaid account's credit", async () => {
    withAvailable(0);
    render(PrepaidBalanceWidget, { billingModel: "postpaid" });
    await screen.findByText("Prepaid Balance");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByText(/Prepaid balance exhausted/)).toBeNull();
    expect(screen.queryByText(/running low/)).toBeNull();
  });
});

it("keeps paid continuity visible during bounded grace", async () => {
  withAvailable(-100);
  balance.paymentGrace = {
    active: true,
    endsAt: new Date(Date.now() + 3600000).toISOString(),
    remainingCreditCents: 900,
    mediaRecoveryUntil: null,
  };
  render(PrepaidBalanceWidget, { billingModel: "prepaid" });
  expect(await screen.findByText(/Payment grace is active/)).toBeTruthy();
  expect(screen.getByText(/whichever comes first/).textContent).toContain("9.00");
  expect(screen.queryByText(/Prepaid balance exhausted/)).toBeNull();
});
it("does not promise continuity after the displayed deadline", async () => {
  withAvailable(-100);
  balance.paymentGrace = {
    active: true,
    endsAt: new Date(Date.now() - 1000).toISOString(),
    remainingCreditCents: 900,
    mediaRecoveryUntil: null,
  };
  render(PrepaidBalanceWidget, { billingModel: "prepaid" });
  expect(await screen.findByText(/Prepaid balance exhausted/)).toBeTruthy();
  expect(screen.queryByText(/Payment grace is active/)).toBeNull();
});
it("shows the separate recovery deadline on Free", async () => {
  withAvailable(-100);
  balance.paymentGrace = {
    active: false,
    endsAt: null,
    remainingCreditCents: 0,
    mediaRecoveryUntil: new Date(Date.now() + 86400000).toISOString(),
  };
  render(PrepaidBalanceWidget, { billingModel: "postpaid" });
  expect(await screen.findByText(/previous paid media has a recovery window/)).toBeTruthy();
  expect(screen.queryByText(/Prepaid balance exhausted/)).toBeNull();
});

it("updates the grace notice when its deadline passes without a reload", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  withAvailable(-100);
  balance.paymentGrace = {
    active: true,
    endsAt: new Date(Date.now() + 500).toISOString(),
    remainingCreditCents: 900,
    mediaRecoveryUntil: null,
  };
  render(PrepaidBalanceWidget, { billingModel: "prepaid" });
  await screen.findByText(/Payment grace is active/);
  await act(() => vi.advanceTimersByTime(1000));
  expect(screen.getByText(/Prepaid balance exhausted/)).toBeTruthy();
  expect(screen.queryByText(/Payment grace is active/)).toBeNull();
});
