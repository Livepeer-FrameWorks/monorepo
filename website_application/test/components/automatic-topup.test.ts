import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
const mocks = vi.hoisted(() => ({
  mutate: vi.fn(),
  settings: {
    enabled: false,
    thresholdEurCents: 500,
    netAmountMinor: 2000,
    monthlyGrossLimitMinor: 10000,
    currency: "USD",
    pausedReason: "",
    monthlyReservedOrSpentMinor: 0,
    available: true,
    unavailableReason: "",
    consentVersion: "",
  },
}));
vi.mock("$houdini", () => ({
  GetAutomaticTopupStore: class {
    async fetch() {
      return { data: { automaticTopup: { ...mocks.settings } } };
    }
  },
  SetAutomaticTopupStore: class {
    mutate = mocks.mutate;
  },
}));
const { default: AutomaticTopup } = await import("$lib/components/AutomaticTopup.svelte");
beforeEach(() => {
  mocks.settings.enabled = false;
  mocks.settings.pausedReason = "";
  mocks.mutate.mockReset();
  mocks.mutate.mockImplementation(async ({ input }) => ({
    data: { setAutomaticTopup: { ...mocks.settings, ...input } },
  }));
});
afterEach(cleanup);
describe("automatic top-up consent", () => {
  it("requires explicit authorisation and permits a zero threshold", async () => {
    render(AutomaticTopup);
    const button = await screen.findByRole("button", { name: "Enable automatic top-ups" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect(mocks.mutate).not.toHaveBeenCalled();
    await fireEvent.input(screen.getByLabelText("When EUR balance falls to"), {
      target: { value: "0.00" },
    });
    await fireEvent.click(screen.getByRole("checkbox"));
    await fireEvent.click(button);
    expect(mocks.mutate).toHaveBeenCalledWith({
      input: {
        enabled: true,
        thresholdEurCents: 0,
        netAmountMinor: 2000,
        monthlyGrossLimitMinor: 10000,
        currency: "USD",
        consentVersion: "prepaid_auto_topup_v1",
      },
    });
  });
  it("allows disabling without new consent and explains an in-flight settlement", async () => {
    mocks.settings.enabled = true;
    render(AutomaticTopup);
    await fireEvent.click(await screen.findByRole("button", { name: "Disable automatic top-ups" }));
    expect(mocks.mutate.mock.calls[0][0].input.enabled).toBe(false);
    expect(await screen.findByText(/A payment already in progress may still settle/)).toBeTruthy();
  });
  it("shows the reason automation paused", async () => {
    mocks.settings.pausedReason = "Automatic payment required authentication. Add funds manually.";
    render(AutomaticTopup);
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      mocks.settings.pausedReason
    );
  });
});
