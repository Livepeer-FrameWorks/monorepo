import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/svelte";

let balance = { balanceCents: 0, reservedBalanceCents: 0, availableBalanceCents: 0 };

vi.mock("$houdini", () => ({
  GetPrepaidBalanceStore: class {
    async fetch() {
      return { data: { prepaidBalance: { ...balance, currency: "EUR", isLowBalance: true } } };
    }
  },
}));

const { default: PrepaidBalanceWidget } =
  await import("$lib/components/PrepaidBalanceWidget.svelte");

afterEach(() => cleanup());

function withAvailable(cents: number) {
  balance = { balanceCents: cents, reservedBalanceCents: 0, availableBalanceCents: cents };
}

describe("prepaid balance widget", () => {
  it("states the spent-balance rule at exactly €0, where Purser starts refusing", async () => {
    withAvailable(0);
    render(PrepaidBalanceWidget, { billingModel: "prepaid" });
    const notice = await screen.findByText(/Balance spent/);
    expect(notice.textContent).toContain("Running streams and their viewers continue");
    expect(notice.textContent).toContain("cannot reconnect");
    expect(notice.textContent).toContain("-€10");
  });

  it("states the suspension when Purser suspended the account", async () => {
    withAvailable(-1500);
    render(PrepaidBalanceWidget, { billingModel: "prepaid", suspended: true });
    expect(await screen.findByText(/Your account is suspended/)).toBeTruthy();
  });

  it("does not call an account suspended on a reservation alone", async () => {
    // Settled +€5 with €20 reserved: available is -€15, but Purser suspends
    // on the settled balance, so the account is only refusing new sessions.
    balance = { balanceCents: 500, reservedBalanceCents: 2000, availableBalanceCents: -1500 };
    render(PrepaidBalanceWidget, { billingModel: "prepaid", suspended: false });
    expect(await screen.findByText(/Balance spent/)).toBeTruthy();
    expect(screen.queryByText(/suspended/)).toBeNull();
  });

  it("shows no spent-balance rule for a postpaid account's credit", async () => {
    withAvailable(0);
    render(PrepaidBalanceWidget, { billingModel: "postpaid" });
    await screen.findByText("Prepaid Balance");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByText(/Balance spent/)).toBeNull();
    expect(screen.queryByText(/Low balance/)).toBeNull();
  });
});
