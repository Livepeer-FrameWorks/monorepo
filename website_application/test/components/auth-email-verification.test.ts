import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { resolve } from "$app/paths";
import { page } from "$app/state";
import type { ResolvedPathname } from "$app/types";
import VerifyEmailPage from "../../src/routes/verify-email/+page.svelte";
import ResetPasswordPage from "../../src/routes/reset-password/+page.svelte";

// The page URL after a navigation to pathname, carrying the current hash.
function pageURL(pathname: ResolvedPathname): URL & { pathname: ResolvedPathname } {
  return Object.assign(new URL(window.location.href), { pathname });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/");
});

describe("email verification link", () => {
  it("posts a fragment token in the request body and removes it from the URL", async () => {
    const token = "test-verification-token";
    const pathname = resolve("/verify-email");
    window.history.replaceState({}, "", `${pathname}#token=${token}`);
    page.url = pageURL(pathname);
    const fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ message: "Email verified" }),
    });
    vi.stubGlobal("fetch", fetch);

    render(VerifyEmailPage);

    await waitFor(() => {
      expect(fetch).toHaveBeenCalledWith("/auth/verify", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
    });
    expect(window.location.href).not.toContain(token);
  });

  it("accepts a password-reset fragment without leaving its token in the URL", async () => {
    const token = "test-reset-token";
    const pathname = resolve("/reset-password");
    window.history.replaceState({}, "", `${pathname}#token=${token}`);
    page.url = pageURL(pathname);

    render(ResetPasswordPage);

    await waitFor(() => expect(screen.getByLabelText("New Password")).toBeTruthy());
    expect(window.location.href).not.toContain(token);
  });
});
