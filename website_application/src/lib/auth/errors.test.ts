import { describe, expect, it } from "vitest";

import { EMAIL_NOT_VERIFIED_ERROR_CODE, isEmailVerificationRequired } from "./errors";

describe("auth error classification", () => {
  it("detects the stable email verification code", () => {
    expect(isEmailVerificationRequired({ errorCode: EMAIL_NOT_VERIFIED_ERROR_CODE })).toBe(true);
    expect(isEmailVerificationRequired({ code: EMAIL_NOT_VERIFIED_ERROR_CODE.toLowerCase() })).toBe(
      true
    );
  });

  it("detects verification and activation wording", () => {
    expect(isEmailVerificationRequired({ error: "email not verified" })).toBe(true);
    expect(
      isEmailVerificationRequired({ error: "please verify your email before signing in" })
    ).toBe(true);
    expect(isEmailVerificationRequired({ error: "account not activated" })).toBe(true);
    expect(isEmailVerificationRequired({ error: "please activate your account first" })).toBe(true);
  });

  it("does not treat deactivated accounts or invalid credentials as verification", () => {
    expect(isEmailVerificationRequired({ error: "account deactivated" })).toBe(false);
    expect(isEmailVerificationRequired({ error: "invalid credentials" })).toBe(false);
  });
});
