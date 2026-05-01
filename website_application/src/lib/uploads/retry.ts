export type RetryDecision = "retry" | "fail_fast";

export interface RetryError {
  status?: number;
  name?: string;
  message?: string;
}

export const MAX_ATTEMPTS = 5;
const BASE_DELAY_MS = 500;
const MAX_DELAY_MS = 30_000;

export function classify(err: RetryError): RetryDecision {
  if (err.name === "AbortError") return "fail_fast";

  const status = err.status;
  if (status == null) {
    // No HTTP status: network error, DNS, TLS, fetch threw. Retry.
    return "retry";
  }

  if (status === 408 || status === 429) return "retry";
  if (status >= 500 && status < 600) return "retry";
  // Includes 403 SignatureDoesNotMatch / expired URL — presigned URL won't recover from a retry.
  return "fail_fast";
}

export function backoffDelayMs(attempt: number, rng: () => number = Math.random): number {
  const exp = Math.min(MAX_DELAY_MS, BASE_DELAY_MS * 2 ** Math.max(0, attempt - 1));
  return Math.floor(rng() * exp);
}

export function shouldRetry(err: RetryError, attempt: number): boolean {
  if (attempt >= MAX_ATTEMPTS) return false;
  return classify(err) === "retry";
}
