import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import TenantBillingActions from "$lib/components/admin/TenantBillingActions.svelte";

const mocks = vi.hoisted(() => ({
  credit: vi.fn(),
  assign: vi.fn(),
  preview: vi.fn(),
  refund: vi.fn(),
  automatic: vi.fn(),
  disable: vi.fn(),
  recover: vi.fn(),
  terms: vi.fn(),
}));
vi.mock("$houdini", () => ({
  PlatformAdjustBalanceStore: class {
    mutate = mocks.credit;
  },
  PlatformAssignBillingTierWithReviewStore: class {
    mutate = mocks.assign;
  },
  PlatformPreviewBillingTierStore: class {
    mutate = mocks.preview;
  },
  PlatformRefundInvoiceStore: class {
    mutate = mocks.refund;
  },
  GetPlatformAutomaticTopupStore: class {
    fetch = mocks.automatic;
  },
  PlatformDisableAutomaticTopupStore: class {
    mutate = mocks.disable;
  },
  PlatformRecoverCheckoutStore: class {
    mutate = mocks.recover;
  },
  PlatformSetEntitlementsStore: class {
    mutate = mocks.terms;
  },
}));
const tenantId = "11111111-1111-4111-8111-111111111111";
beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.clear();
});
afterEach(cleanup);

it("preserves one immutable credit request across an uncertain response and reload", async () => {
  mocks.credit.mockRejectedValueOnce(new Error("connection lost")).mockResolvedValueOnce({
    data: {
      platformAdjustBalance: { id: "ledger-1", amountCents: 7900, balanceAfterCents: 8812 },
    },
  });
  render(TenantBillingActions, { tenantId });
  await fireEvent.input(screen.getByLabelText("EUR amount (negative removes credit)"), {
    target: { value: "79.00" },
  });
  await fireEvent.input(screen.getByLabelText("Audit reason"), {
    target: { value: "incident credit" },
  });
  await fireEvent.submit(document.getElementById("operator-billing-change")!);
  await screen.findByRole("button", { name: "Apply reviewed change" });
  expect(mocks.credit).not.toHaveBeenCalled();
  await fireEvent.click(screen.getByRole("button", { name: "Apply reviewed change" }));
  await screen.findByText("connection lost");
  const first = mocks.credit.mock.calls[0][0];
  expect(first).toMatchObject({ tenantId, amountCents: 7900, reason: "incident credit" });
  expect(first.referenceId).toMatch(/^[a-f0-9-]{36}$/);
  cleanup();
  render(TenantBillingActions, { tenantId });
  await fireEvent.click(await screen.findByRole("button", { name: "Retry same operation" }));
  await screen.findByText(/Ledger receipt ledger-1/);
  expect(mocks.credit.mock.calls[1][0]).toEqual(first);
  expect(sessionStorage.getItem(`frameworks:operator-credit:${tenantId}`)).toBeNull();
});

it("requires a provider preview before confirming checkout recovery", async () => {
  mocks.recover.mockResolvedValue({
    data: {
      platformRecoverCheckout: {
        provider: "stripe",
        status: "open",
        amountCents: 0,
        currency: "EUR",
        amountKnown: false,
        outcome: "preview",
        detail: "setup checkout",
        auditId: "",
      },
    },
  });
  render(TenantBillingActions, { tenantId });
  await fireEvent.change(screen.getByLabelText("Action"), { target: { value: "checkout" } });
  await fireEvent.input(screen.getByLabelText("Checkout reservation ID"), {
    target: { value: "11111111-2222-4333-8444-555555555555" },
  });
  await fireEvent.input(screen.getByLabelText("Audit reason"), {
    target: { value: "duplicate checkout" },
  });
  await fireEvent.submit(document.getElementById("operator-billing-change")!);
  await screen.findByText(/Amount requires provider inspection/);
  expect(mocks.recover).toHaveBeenCalledWith(expect.objectContaining({ confirmed: false }));
  expect(mocks.recover).toHaveBeenCalledTimes(1);
  await fireEvent.click(screen.getByRole("button", { name: "Apply reviewed change" }));
  await waitFor(() =>
    expect(mocks.recover).toHaveBeenLastCalledWith(expect.objectContaining({ confirmed: true }))
  );
});

it("sends the reviewed entitlement map with explicit zero and false", async () => {
  mocks.terms.mockResolvedValue({ data: { updateSubscriptionCustomTerms: { id: "sub" } } });
  const original = [{ key: "recording_retention_days", value: "30" }];
  render(TenantBillingActions, { tenantId, entitlements: original });
  await fireEvent.change(screen.getByLabelText("Action"), { target: { value: "entitlements" } });
  await fireEvent.input(screen.getByLabelText("Complete override map (JSON)"), {
    target: { value: '{"recording_retention_days":0,"custom_domain_enabled":false}' },
  });
  await fireEvent.input(screen.getByLabelText("Audit reason"), {
    target: { value: "recording policy" },
  });
  await fireEvent.submit(document.getElementById("operator-billing-change")!);
  await fireEvent.click(await screen.findByRole("button", { name: "Apply reviewed change" }));
  await waitFor(() =>
    expect(mocks.terms).toHaveBeenCalledWith({
      tenantId,
      input: {
        reason: "recording policy",
        expectedEntitlementOverrides: original,
        entitlementOverrides: [
          { key: "recording_retention_days", value: "0" },
          { key: "custom_domain_enabled", value: "false" },
        ],
      },
    })
  );
});

it("clears only the submitted tenant's retry record when props change during a credit", async () => {
  const otherTenant = "22222222-2222-4222-8222-222222222222";
  const otherKey = `frameworks:operator-credit:${otherTenant}`;
  sessionStorage.setItem(otherKey, "another pending operation");
  let complete!: (response: unknown) => void;
  mocks.credit.mockReset().mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        complete = resolve;
      })
  );
  const onChanged = vi.fn();
  const view = render(TenantBillingActions, { tenantId, onChanged });
  await fireEvent.input(screen.getByLabelText("EUR amount (negative removes credit)"), {
    target: { value: "79.00" },
  });
  await fireEvent.input(screen.getByLabelText("Audit reason"), {
    target: { value: "incident credit" },
  });
  await fireEvent.submit(document.getElementById("operator-billing-change")!);
  await fireEvent.click(await screen.findByRole("button", { name: "Apply reviewed change" }));
  await waitFor(() => expect(mocks.credit).toHaveBeenCalledTimes(1));
  await view.rerender({ tenantId: otherTenant });
  complete({
    data: { platformAdjustBalance: { id: "ledger-a", amountCents: 7900, balanceAfterCents: 8812 } },
  });
  await waitFor(() =>
    expect(sessionStorage.getItem(`frameworks:operator-credit:${tenantId}`)).toBeNull()
  );
  expect(sessionStorage.getItem(otherKey)).toBe("another pending operation");
  expect(mocks.credit.mock.calls[0][0].tenantId).toBe(tenantId);
  expect(screen.queryByText(/Ledger receipt ledger-a/)).toBeNull();
  expect(onChanged).not.toHaveBeenCalled();
});

it("prices the complete arrangement before applying its exact reviewed version", async () => {
  const quote = {
    quotedAt: "2026-11-26T12:00:00Z",
    expiresAt: "2026-11-26T12:05:00Z",
    fingerprint: "reviewed-version",
    effectsJson: JSON.stringify({
      wallet_before_eur_cents: 912,
      wallet_after_eur_cents: 900,
      prepaid: { statement_settlement_eur_cents: 0, statement_fees_eur_cents: 0 },
      postpaid: {
        invoice_due_eur_cents: 100,
        invoice_wallet_credit_eur_cents: 12,
        invoice_presentment_currency: "EUR",
        invoice_presentment_minor: 100,
        collection_carry_eur_cents: 0,
        unpaid_advance_reduction_eur_cents: 0,
        paid_advance_forfeiture_eur_cents: 7600,
      },
    }),
  };
  mocks.preview.mockResolvedValue({ data: { platformPreviewBillingTier: quote } });
  mocks.assign.mockResolvedValue({
    data: {
      platformAssignBillingTierWithReview: {
        subscriptionId: "subscription",
        tierName: "payg",
        billingModel: "prepaid",
        changed: true,
      },
    },
  });
  render(TenantBillingActions, { tenantId });
  await fireEvent.change(screen.getByLabelText("Action"), { target: { value: "tier" } });
  await fireEvent.input(screen.getByLabelText("Catalogue tier name"), {
    target: { value: "payg" },
  });
  await fireEvent.input(screen.getByLabelText("Audit reason"), {
    target: { value: "reviewed customer repair" },
  });
  await fireEvent.submit(document.getElementById("operator-billing-change")!);
  await screen.findByText("Unused paid fee retained");
  expect(mocks.assign).not.toHaveBeenCalled();
  await fireEvent.click(screen.getByRole("button", { name: "Apply reviewed change" }));
  await waitFor(() =>
    expect(mocks.assign).toHaveBeenCalledWith({
      tenantId,
      tierName: "payg",
      reason: "reviewed customer repair",
      quotedAt: quote.quotedAt,
      fingerprint: quote.fingerprint,
    })
  );
});

it("keeps the same cash-refund reference through a lost response, pending bank return and reload", async () => {
  let confirmations = 0;
  mocks.refund.mockReset().mockImplementation(async (request) => {
    const receipt = {
      requestId: request.requestId,
      invoiceId: request.invoiceId,
      provider: "stripe",
      amountMinor: 8661,
      currency: "USD",
      fingerprint: "refund-version",
      detail: "Full original payment. No wallet credit.",
      providerRefundId: "",
    };
    if (!request.confirmed)
      return { data: { platformRefundInvoice: { ...receipt, status: "preview" } } };
    confirmations++;
    if (confirmations === 1) throw new Error("connection lost");
    return {
      data: {
        platformRefundInvoice: {
          ...receipt,
          status: confirmations === 2 ? "submitted" : "succeeded",
        },
      },
    };
  });
  render(TenantBillingActions, { tenantId });
  await fireEvent.change(screen.getByLabelText("Action"), { target: { value: "refund" } });
  await fireEvent.input(screen.getByLabelText("Paid invoice UUID"), {
    target: { value: "11111111-2222-4333-8444-555555555555" },
  });
  await fireEvent.input(screen.getByLabelText("Audit reason"), {
    target: { value: "approved original payment return" },
  });
  await fireEvent.submit(document.getElementById("operator-billing-change")!);
  await fireEvent.click(await screen.findByRole("button", { name: "Apply reviewed change" }));
  await screen.findByText("connection lost");
  const original = mocks.refund.mock.calls[1][0];
  expect(original.confirmed).toBe(true);
  expect(original.fingerprint).toBe("refund-version");
  cleanup();
  render(TenantBillingActions, { tenantId });
  await fireEvent.click(await screen.findByRole("button", { name: "Retry same operation" }));
  await screen.findByText(/submitted/);
  expect(sessionStorage.getItem(`frameworks:operator-credit:${tenantId}`)).not.toBeNull();
  await fireEvent.click(screen.getByRole("button", { name: "Retry same operation" }));
  await screen.findByText(/succeeded/);
  expect(mocks.refund.mock.calls[2][0]).toEqual(original);
  expect(mocks.refund.mock.calls[3][0]).toEqual(original);
  expect(sessionStorage.getItem(`frameworks:operator-credit:${tenantId}`)).toBeNull();
});

it("only disables automatic payments after inspecting the target tenant and confirming", async () => {
  mocks.automatic.mockResolvedValue({
    data: {
      platformAutomaticTopup: {
        enabled: true,
        monthlyReservedOrSpentMinor: 2000,
        currency: "USD",
        pausedReason: "",
      },
    },
  });
  mocks.disable.mockResolvedValue({ data: { platformDisableAutomaticTopup: { enabled: false } } });
  render(TenantBillingActions, { tenantId });
  await fireEvent.change(screen.getByLabelText("Action"), { target: { value: "disable_auto" } });
  await fireEvent.input(screen.getByLabelText("Audit reason"), {
    target: { value: "customer requested stop" },
  });
  await fireEvent.submit(document.getElementById("operator-billing-change")!);
  await screen.findByText(/reserved or spent this UTC month/);
  expect(mocks.disable).not.toHaveBeenCalled();
  await fireEvent.click(screen.getByRole("button", { name: "Apply reviewed change" }));
  await waitFor(() =>
    expect(mocks.disable).toHaveBeenCalledWith({ tenantId, reason: "customer requested stop" })
  );
});
