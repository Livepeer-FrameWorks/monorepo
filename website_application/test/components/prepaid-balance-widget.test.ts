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

  it("says running streams stop below -€10", async () => {
    withAvailable(-1500);
    render(PrepaidBalanceWidget, { billingModel: "prepaid" });
    expect(await screen.findByText(/Below -€10 the account is suspended/)).toBeTruthy();
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
