export const EMAIL_NOT_VERIFIED_ERROR_CODE = "EMAIL_NOT_VERIFIED";

interface AuthFailure {
  error?: string;
  errorCode?: string;
  code?: string;
}

const EMAIL_VERIFICATION_MESSAGES = [
  "not verified",
  "verify your email",
  "not activated",
  "activate your account",
  "activate your email",
];

export function isEmailVerificationRequired(result: AuthFailure): boolean {
  const code = (result.errorCode ?? result.code)?.trim().toUpperCase();
  if (code === EMAIL_NOT_VERIFIED_ERROR_CODE) {
    return true;
  }

  const message = result.error?.toLowerCase() ?? "";
  return EMAIL_VERIFICATION_MESSAGES.some((indicator) => message.includes(indicator));
}
