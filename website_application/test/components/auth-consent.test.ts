import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { tick } from "svelte";
import { page } from "$app/state";
import DevicePage from "../../src/routes/device/+page.svelte";
import AuthorizePage from "../../src/routes/authorize/+page.svelte";

const mocks = vi.hoisted(() => ({ post: vi.fn(), navigate: vi.fn() }));
vi.mock("$lib/authAPI", () => ({ authAPI: { post: mocks.post } }));
vi.mock("$lib/auth/consent", async (original) => ({
  ...(await original<typeof import("$lib/auth/consent")>()),
  returnToAuthorizationClient: mocks.navigate,
}));
function pageURL(value: string): typeof page.url {
  return new URL(value) as typeof page.url;
}
function preview(overrides: Record<string, unknown> = {}) {
  return {
    client_id: "cli",
    scope: "account",
    device_label: "Original workstation",
    client_version: "v0.3.15 linux/arm64",
    request_ip: "203.0.113.14",
    location: "Example City",
    user_agent: "native-agent",
    requested_at: new Date(Date.now() - 30_000).toISOString(),
    expires_at: new Date(Date.now() + 60_000).toISOString(),
    ...overrides,
  };
}
function nativePreview(overrides: Record<string, unknown> = {}) {
  return preview({
    client_id: "tray-mac",
    redirect_uri: "http://127.0.0.1:54321/callback",
    code_challenge: "signed-challenge",
    code_challenge_method: "S256",
    state: "signed-state",
    ...overrides,
  });
}
beforeEach(() => {
  vi.resetAllMocks();
  page.url = pageURL("http://localhost/");
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("native sign-in consent", () => {
  it("shows original CLI metadata, including codes containing 0/1/8/9, and approves that code", async () => {
    page.url = pageURL("http://localhost/device?user_code=a019-b239");
    mocks.post.mockImplementation(async (path: string) =>
      path === "/device/lookup" ? { data: preview() } : { data: { success: true } }
    );
    render(DevicePage);
    await screen.findByText("Original workstation");
    expect(screen.getByText("203.0.113.14")).toBeTruthy();
    expect(screen.getByText("v0.3.15 linux/arm64")).toBeTruthy();
    expect(screen.getByText("Example City")).toBeTruthy();
    expect(mocks.post).toHaveBeenCalledWith("/device/lookup", { user_code: "A019-B239" });
    await fireEvent.click(screen.getByRole("button", { name: "Approve sign-in" }));
    await screen.findByText("Sign-in approved");
    expect(mocks.post).toHaveBeenCalledWith("/device/approve", { user_code: "A019-B239" });
  });
  it("uses neutral approval language for a tray device grant", async () => {
    page.url = pageURL("http://localhost/device?user_code=ABCD-EFGH");
    mocks.post.mockResolvedValue({ data: preview({ client_id: "tray-mac" }) });
    render(DevicePage);
    await screen.findByText("FrameWorks macOS tray");
    expect(screen.queryByText("Authorize CLI")).toBeNull();
    expect(screen.getByRole("button", { name: "Approve sign-in" })).toBeTruthy();
  });
  it("cannot approve an expired request", async () => {
    page.url = pageURL("http://localhost/device?user_code=ABCD-EFGH");
    mocks.post.mockResolvedValue({
      data: preview({ expires_at: new Date(Date.now() - 1000).toISOString() }),
    });
    render(DevicePage);
    await screen.findByText("This request expired. Start sign-in again from your app.");
    const approve = screen.getByRole("button", { name: "Approve sign-in" });
    expect(approve.hasAttribute("disabled")).toBe(true);
    await fireEvent.click(approve);
    expect(mocks.post).toHaveBeenCalledTimes(1);
  });
  it("does not approve a new code using a stale lookup of another device", async () => {
    page.url = pageURL("http://localhost/device?user_code=AAAA-1111");
    let resolveOld: (value: { data: ReturnType<typeof preview> }) => void = () => {};
    mocks.post.mockImplementation((path: string, body: { user_code: string }) => {
      if (path === "/device/approve") return Promise.resolve({ data: { success: true } });
      if (body.user_code === "AAAA-1111")
        return new Promise((resolve) => {
          resolveOld = resolve;
        });
      return Promise.resolve({ data: preview({ device_label: "New workstation" }) });
    });
    render(DevicePage);
    await waitFor(() => expect(mocks.post).toHaveBeenCalledTimes(1));
    await fireEvent.input(screen.getByLabelText("Device code"), { target: { value: "B2223333" } });
    await fireEvent.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText("New workstation");
    resolveOld({ data: preview({ device_label: "Old workstation" }) });
    await tick();
    expect(screen.queryByText("Old workstation")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Approve sign-in" }));
    await waitFor(() =>
      expect(mocks.post).toHaveBeenCalledWith("/device/approve", { user_code: "B222-3333" })
    );
  });
  it("uses verified PKCE bindings and ignores forged query metadata", async () => {
    page.url = pageURL(
      "http://localhost/authorize?request_context=signed-context&client_id=forged&redirect_uri=https%3A%2F%2Fevil.example%2Fcallback&state=forged&device_label=Forged"
    );
    mocks.post.mockImplementation(async (path: string) =>
      path === "/authorize/lookup" ? { data: nativePreview() } : { data: { code: "one-use-code" } }
    );
    render(AuthorizePage);
    await screen.findByText("Original workstation");
    expect(screen.queryByText("Forged")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Approve sign-in" }));
    await waitFor(() =>
      expect(mocks.post).toHaveBeenCalledWith("/authorize/complete", {
        client_id: "tray-mac",
        redirect_uri: "http://127.0.0.1:54321/callback",
        code_challenge: "signed-challenge",
        code_challenge_method: "S256",
        scope: "account",
        state: "signed-state",
        request_context: "signed-context",
      })
    );
    const target = mocks.navigate.mock.calls[0][0] as URL;
    expect(target.hostname).toBe("127.0.0.1");
    expect(target.searchParams.get("state")).toBe("signed-state");
    expect(target.searchParams.get("code")).toBe("one-use-code");
  });
  it("does not fall back to unverified legacy parameters after context verification fails", async () => {
    page.url = pageURL(
      "http://localhost/authorize?request_context=bad&client_id=tray-mac&redirect_uri=http%3A%2F%2F127.0.0.1%3A54321%2Fcallback&code_challenge=legacy&code_challenge_method=S256"
    );
    mocks.post.mockRejectedValue({
      response: { data: { error: "invalid or expired authorization request" } },
    });
    render(AuthorizePage);
    await screen.findByText("invalid or expired authorization request");
    const approve = screen.getByRole("button", { name: "Approve sign-in" });
    expect(approve.hasAttribute("disabled")).toBe(true);
    await fireEvent.click(approve);
    expect(mocks.post).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).not.toHaveBeenCalled();
  });
  it("retains legacy PKCE approval without inventing native provenance", async () => {
    page.url = pageURL(
      "http://localhost/authorize?client_id=tray-mac&redirect_uri=http%3A%2F%2F127.0.0.1%3A54321%2Fcallback&code_challenge=legacy&code_challenge_method=S256&state=legacy-state&device_label=Forged&request_ip=192.0.2.1"
    );
    mocks.post.mockResolvedValue({ data: { code: "legacy-code" } });
    render(AuthorizePage);
    await screen.findByText(/Device details and request expiry are unavailable/);
    expect(screen.queryByText("Forged")).toBeNull();
    expect(screen.queryByText("192.0.2.1")).toBeNull();
    expect(mocks.post).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Approve sign-in" }));
    await waitFor(() => expect(mocks.navigate).toHaveBeenCalledTimes(1));
    expect(mocks.post.mock.calls[0][1]).not.toHaveProperty("request_context");
  });
  it("denies only a valid loopback request and preserves its state", async () => {
    page.url = pageURL(
      "http://localhost/authorize?client_id=tray-mac&redirect_uri=http%3A%2F%2F127.0.0.1%3A54321%2Fcallback&code_challenge=legacy&code_challenge_method=S256&state=denied-state"
    );
    render(AuthorizePage);
    await fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    const target = mocks.navigate.mock.calls[0][0] as URL;
    expect(target.searchParams.get("error")).toBe("access_denied");
    expect(target.searchParams.get("state")).toBe("denied-state");
    expect(mocks.post).not.toHaveBeenCalled();
    cleanup();
    mocks.navigate.mockClear();
    page.url = pageURL(
      "http://localhost/authorize?client_id=tray-mac&redirect_uri=https%3A%2F%2Fevil.example%2Fcallback&code_challenge=legacy&code_challenge_method=S256"
    );
    render(AuthorizePage);
    expect(screen.queryByRole("button", { name: "Deny" })).toBeNull();
    expect(screen.getByRole("button", { name: "Approve sign-in" }).hasAttribute("disabled")).toBe(
      true
    );
    expect(mocks.navigate).not.toHaveBeenCalled();
  });
  it("does not navigate when approval returned no code", async () => {
    page.url = pageURL("http://localhost/authorize?request_context=signed-context");
    mocks.post.mockImplementation(async (path: string) =>
      path === "/authorize/lookup" ? { data: nativePreview() } : { data: {} }
    );
    render(AuthorizePage);
    await screen.findByText("Original workstation");
    await fireEvent.click(screen.getByRole("button", { name: "Approve sign-in" }));
    await screen.findByText("The service did not return an approval code. Try again.");
    expect(mocks.navigate).not.toHaveBeenCalled();
  });
});
