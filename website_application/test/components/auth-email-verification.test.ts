import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { page } from "$app/state";
import VerifyEmailPage from "../../src/routes/verify-email/+page.svelte";
import ResetPasswordPage from "../../src/routes/reset-password/+page.svelte";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/");
});

describe("email verification link", () => {
  it("posts a fragment token in the request body and removes it from the URL", async () => {
    const token = "test-verification-token";
    window.history.replaceState({}, "", `/verify-email#token=${token}`);
    page.url = new URL(window.location.href);
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
    window.history.replaceState({}, "", `/reset-password#token=${token}`);
    page.url = new URL(window.location.href);

    render(ResetPasswordPage);

    await waitFor(() => expect(screen.getByLabelText("New Password")).toBeTruthy());
    expect(window.location.href).not.toContain(token);
  });
});
