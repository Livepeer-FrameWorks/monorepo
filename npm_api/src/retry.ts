/** When and how long the client waits before sending a failed request again. */
export interface RetryPolicy {
  /** Attempts in total, including the first. 1 disables retries. */
  maxAttempts: number;
  /** Wait before the first retry; each later retry doubles it. */
  baseDelayMs: number;
  /** Upper bound of the doubled wait. */
  maxDelayMs: number;
  /** A Retry-After longer than this ends retrying instead of waiting. */
  maxRetryAfterMs: number;
  /** Randomize each wait between half and all of it, so clients do not retry in step. */
  jitter: boolean;
}

export const defaultRetryPolicy: Readonly<RetryPolicy> = Object.freeze({
  maxAttempts: 3,
  baseDelayMs: 250,
  maxDelayMs: 4000,
  maxRetryAfterMs: 30_000,
  jitter: true,
});

/** HTTP statuses that are retried: timeouts, throttling, and transient server failures. */
export const retryableStatuses: ReadonlySet<number> = new Set([408, 429, 500, 502, 503, 504]);

/** The wait before retry number `retry` (1 for the first retry). */
export function backoffDelay(
  policy: RetryPolicy,
  retry: number,
  random: () => number = Math.random
): number {
  const delay = Math.min(policy.maxDelayMs, policy.baseDelayMs * 2 ** (retry - 1));
  if (!policy.jitter) {
    return delay;
  }
  return Math.round(delay / 2 + random() * (delay / 2));
}

/**
 * Parses a Retry-After value: delta seconds or an HTTP date. Returns whole
 * seconds, or null when the value is missing or unparseable.
 */
export function parseRetryAfter(value: string | null, now: number = Date.now()): number | null {
  if (value === null) {
    return null;
  }
  const trimmed = value.trim();
  if (/^\d+$/.test(trimmed)) {
    return Number.parseInt(trimmed, 10);
  }
  const date = Date.parse(trimmed);
  if (Number.isNaN(date) || !/[a-z]/i.test(trimmed)) {
    return null;
  }
  return Math.max(0, Math.ceil((date - now) / 1000));
}

export type Sleep = (ms: number, signal?: AbortSignal) => Promise<void>;

export const defaultSleep: Sleep = (ms, signal) =>
  new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason);
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal?.reason);
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
