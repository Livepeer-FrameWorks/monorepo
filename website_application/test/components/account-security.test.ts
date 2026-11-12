import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { tick } from "svelte";
import SecurityPage from "../../src/routes/account/security/+page.svelte";

const mocks = vi.hoisted(() => ({
  sessions: vi.fn(),
  activity: vi.fn(),
  identity: vi.fn(),
  revoke: vi.fn(),
  others: vi.fn(),
  rename: vi.fn(),
  password: vi.fn(),
  linkEmail: vi.fn(),
  linkWallet: vi.fn(),
  unlinkWallet: vi.fn(),
  logout: vi.fn(),
  clear: vi.fn(),
  checkAuth: vi.fn(),
  challenge: vi.fn(),
  goto: vi.fn(),
  connect: vi.fn(),
  connectors: vi.fn(),
  sign: vi.fn(),
  disconnect: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("$app/navigation", () => ({ goto: mocks.goto }));
vi.mock("$lib/stores/auth", async () => {
  const { readable } = await import("svelte/store");
  return {
    auth: {
      subscribe: readable({ isAuthenticated: true, user: { email: "owner@example.test" } })
        .subscribe,
      logout: mocks.logout,
      clearLocalSession: mocks.clear,
      checkAuth: mocks.checkAuth,
      issueWalletChallenge: mocks.challenge,
    },
  };
});
vi.mock("$lib/stores/toast", () => ({ toast: { success: mocks.toast, error: mocks.toast } }));
vi.mock("wagmi/actions", () => ({ connect: mocks.connect, getConnectors: mocks.connectors }));
vi.mock("$lib/wallet/config", () => ({ wagmiConfig: {} }));
vi.mock("$lib/wallet/store.svelte", () => ({
  setupWalletWatcher: vi.fn(),
  cleanupWalletWatcher: vi.fn(),
  signAuthMessage: mocks.sign,
  disconnectWallet: mocks.disconnect,
}));
vi.mock("$lib/account-security", async (original) => {
  const module = await original<typeof import("$lib/account-security")>();
  return {
    ...module,
    loadSecuritySessions: mocks.sessions,
    loadSecurityActivity: mocks.activity,
    loadSecurityIdentity: mocks.identity,
    revokeSecuritySession: mocks.revoke,
    revokeOtherSecuritySessions: mocks.others,
    renameSecuritySession: mocks.rename,
    changeSecurityPassword: mocks.password,
    linkSecurityEmail: mocks.linkEmail,
    linkSecurityWallet: mocks.linkWallet,
    unlinkSecurityWallet: mocks.unlinkWallet,
  };
});
const browser = {
  id: "browser",
  clientKind: "BROWSER",
  clientId: "web",
  authMethod: "PASSWORD",
  label: "This browser",
  clientVersion: null,
  ipAddress: "203.0.113.x",
  location: "Example City",
  createdAt: "2026-11-11T12:00:00Z",
  lastActiveAt: "2026-11-12T12:00:00Z",
  expiresAt: "2026-12-12T12:00:00Z",
  current: true,
};
const laptop = {
  ...browser,
  id: "laptop",
  clientKind: "CLI",
  clientId: "cli",
  authMethod: "DEVICE_CODE",
  label: "Lost laptop",
  current: false,
};
const wallets = [
  { id: "wallet-a", address: "0xaaa", createdAt: "2026-11-11T12:00:00Z" },
  { id: "wallet-b", address: "0xbbb", createdAt: "2026-11-11T12:00:00Z" },
];
function sessionPage(nodes = [browser, laptop]) {
  return { nodes, totalCount: nodes.length, pageInfo: { endCursor: null, hasNextPage: false } };
}
function identity(email = "owner@example.test", emailVerified = true, rows = wallets) {
  return { email, emailVerified, wallets: rows };
}
beforeEach(() => {
  vi.clearAllMocks();
  mocks.sessions.mockResolvedValue(sessionPage());
  mocks.activity.mockResolvedValue({
    nodes: [],
    pageInfo: { endCursor: null, hasNextPage: false },
    totalCount: 0,
  });
  mocks.identity.mockResolvedValue(identity());
  mocks.revoke.mockResolvedValue(false);
  mocks.others.mockResolvedValue(1);
  mocks.rename.mockResolvedValue(undefined);
  mocks.password.mockResolvedValue(1);
  mocks.linkEmail.mockResolvedValue(undefined);
  mocks.unlinkWallet.mockResolvedValue(undefined);
  mocks.linkWallet.mockResolvedValue(undefined);
  mocks.logout.mockResolvedValue({ success: true });
  mocks.checkAuth.mockResolvedValue(undefined);
  mocks.goto.mockResolvedValue(undefined);
  mocks.connectors.mockReturnValue([]);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});
async function loaded() {
  render(SecurityPage);
  await screen.findByText("Lost laptop");
  await screen.findByLabelText("Current password");
}
async function openLostLaptop() {
  await fireEvent.click(screen.getByRole("button", { name: "Sign out Lost laptop" }));
  return await screen.findByRole("dialog");
}

describe("Account Security", () => {
  it("keeps session controls available when the activity service fails", async () => {
    mocks.activity.mockRejectedValue(new Error("Activity unavailable"));
    await loaded();
    expect(screen.getByText("This session")).toBeTruthy();
    expect(screen.getAllByText("203.0.113.x").length).toBe(2);
    expect(screen.getByText("Activity unavailable")).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Sign out Lost laptop" }).hasAttribute("disabled")
    ).toBe(false);
  });
  it("requires confirmation and preserves the session and credentials on revoke failure", async () => {
    mocks.revoke.mockRejectedValue(new Error("Revocation unavailable"));
    await loaded();
    const dialog = await openLostLaptop();
    expect(mocks.revoke).not.toHaveBeenCalled();
    await fireEvent.click(within(dialog).getByRole("button", { name: "Confirm sign out" }));
    await within(dialog).findByText("Revocation unavailable");
    expect(screen.getByRole("button", { name: "Sign out Lost laptop" })).toBeTruthy();
    expect(mocks.clear).not.toHaveBeenCalled();
    expect(mocks.goto).not.toHaveBeenCalled();
  });
  it("revokes only the selected session after confirmation", async () => {
    await loaded();
    const dialog = await openLostLaptop();
    mocks.sessions.mockResolvedValue(sessionPage([browser]));
    await fireEvent.click(within(dialog).getByRole("button", { name: "Confirm sign out" }));
    await waitFor(() => expect(mocks.revoke).toHaveBeenCalledWith("laptop"));
    await waitFor(() => expect(screen.queryByText("Lost laptop")).toBeNull());
    expect(screen.getByText("This session")).toBeTruthy();
    expect(mocks.clear).not.toHaveBeenCalled();
  });
  it("uses acknowledged browser logout for this session and keeps failures visible", async () => {
    mocks.logout.mockResolvedValue({ success: false, error: "Sign-out unavailable" });
    await loaded();
    await fireEvent.click(screen.getByRole("button", { name: "Sign out This browser" }));
    const dialog = await screen.findByRole("dialog");
    await fireEvent.click(within(dialog).getByRole("button", { name: "Confirm sign out" }));
    await within(dialog).findByText("Sign-out unavailable");
    expect(mocks.revoke).not.toHaveBeenCalled();
    expect(mocks.clear).not.toHaveBeenCalled();
    expect(mocks.goto).not.toHaveBeenCalled();
    mocks.logout.mockResolvedValue({ success: true });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Confirm sign out" }));
    await waitFor(() => expect(mocks.goto).toHaveBeenCalledWith("/login"));
  });
  it("renames by stable session ID and allows cancel without a mutation", async () => {
    await loaded();
    await fireEvent.click(screen.getByRole("button", { name: "Rename Lost laptop" }));
    let dialog = await screen.findByRole("dialog");
    await fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(mocks.rename).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Rename Lost laptop" }));
    dialog = await screen.findByRole("dialog");
    await fireEvent.input(within(dialog).getByLabelText("Session name"), {
      target: { value: "  Work laptop  " },
    });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Save name" }));
    await waitFor(() => expect(mocks.rename).toHaveBeenCalledWith("laptop", "Work laptop"));
  });
  it("confirms sign-out of other sessions while retaining the current session", async () => {
    await loaded();
    await fireEvent.click(screen.getByRole("button", { name: "Sign out all other sessions" }));
    const dialog = await screen.findByRole("dialog");
    expect(mocks.others).not.toHaveBeenCalled();
    mocks.sessions.mockResolvedValue(sessionPage([browser]));
    await fireEvent.click(within(dialog).getByRole("button", { name: "Confirm sign out" }));
    await waitFor(() => expect(mocks.others).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByText("Lost laptop")).toBeNull());
    expect(screen.getByText("This session")).toBeTruthy();
    expect(mocks.logout).not.toHaveBeenCalled();
  });
  it("validates password confirmation and clears passwords after success", async () => {
    await loaded();
    const current = screen.getByLabelText("Current password");
    const next = screen.getByLabelText("New password");
    const confirmation = screen.getByLabelText("Confirm new password");
    await fireEvent.input(current, { target: { value: "current-password" } });
    await fireEvent.input(next, { target: { value: "new-password" } });
    await fireEvent.input(confirmation, { target: { value: "different-password" } });
    await fireEvent.submit(current.closest("form")!);
    await screen.findByText("Passwords do not match.");
    expect(mocks.password).not.toHaveBeenCalled();
    await fireEvent.input(confirmation, { target: { value: "new-password" } });
    await fireEvent.submit(current.closest("form")!);
    await waitFor(() =>
      expect(mocks.password).toHaveBeenCalledWith("current-password", "new-password")
    );
    await waitFor(() => expect((next as HTMLInputElement).value).toBe(""));
    expect((current as HTMLInputElement).value).toBe("");
    expect((confirmation as HTMLInputElement).value).toBe("");
    expect(mocks.clear).not.toHaveBeenCalled();
  });
  it("offers wallet-only users a link-email password flow", async () => {
    mocks.identity.mockResolvedValue(identity("", false, [wallets[0]]));
    render(SecurityPage);
    const email = await screen.findByLabelText("Email address");
    expect(screen.queryByLabelText("Current password")).toBeNull();
    await fireEvent.input(email, { target: { value: "new@example.test" } });
    await fireEvent.input(screen.getByLabelText("New password"), {
      target: { value: "new-password" },
    });
    await fireEvent.input(screen.getByLabelText("Confirm new password"), {
      target: { value: "new-password" },
    });
    await fireEvent.submit(email.closest("form")!);
    await waitFor(() =>
      expect(mocks.linkEmail).toHaveBeenCalledWith("new@example.test", "new-password")
    );
    expect(mocks.password).not.toHaveBeenCalled();
  });
  it("protects the final wallet and confirms ordinary unlinking", async () => {
    mocks.identity.mockResolvedValue(identity("", false, [wallets[0]]));
    render(SecurityPage);
    const blocked = await screen.findByRole("button", { name: "Unlink wallet 0xaaa" });
    expect(blocked.hasAttribute("disabled")).toBe(true);
    cleanup();
    mocks.identity.mockResolvedValue(identity());
    await loaded();
    await fireEvent.click(screen.getByRole("button", { name: "Unlink wallet 0xaaa" }));
    const dialog = await screen.findByRole("dialog");
    expect(mocks.unlinkWallet).not.toHaveBeenCalled();
    await fireEvent.click(within(dialog).getByRole("button", { name: "Confirm unlink" }));
    await waitFor(() => expect(mocks.unlinkWallet).toHaveBeenCalledWith("wallet-a"));
  });
  it("does not present a failed identity read as no linked wallets", async () => {
    mocks.identity.mockRejectedValue(new Error("Identity unavailable"));
    render(SecurityPage);
    await screen.findByText(/Could not load sign-in methods: Identity unavailable/);
    expect(screen.queryByText("No wallets linked.")).toBeNull();
    expect(screen.getByRole("button", { name: "Sign out Lost laptop" })).toBeTruthy();
  });
  it("appends security activity without repeating mirrored events", async () => {
    const first = {
      id: "a",
      kind: "SIGN_IN_SUCCEEDED",
      timestamp: "2026-11-12T12:00:00Z",
      authMethod: "PASSWORD",
      reason: null,
    };
    const second = { ...first, id: "b", kind: "PASSWORD_CHANGED" };
    mocks.activity
      .mockResolvedValueOnce({
        nodes: [first],
        totalCount: 2,
        pageInfo: { hasNextPage: true, endCursor: "cursor-a" },
      })
      .mockResolvedValueOnce({
        nodes: [first, second],
        totalCount: 2,
        pageInfo: { hasNextPage: false, endCursor: "cursor-b" },
      });
    await loaded();
    await fireEvent.click(screen.getByRole("button", { name: "Load more activity" }));
    await screen.findByText("Password changed");
    expect(mocks.activity).toHaveBeenLastCalledWith("cursor-a");
    expect(screen.getAllByText("Signed in").length).toBe(1);
    await tick();
  });
});
