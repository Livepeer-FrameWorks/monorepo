import { describe, expect, it } from "vitest";
import { backoffDelayMs, classify, MAX_ATTEMPTS, shouldRetry } from "./retry";

describe("classify", () => {
  it("retries on no status (network/DNS/TLS)", () => {
    expect(classify({})).toBe("retry");
    expect(classify({ message: "fetch failed" })).toBe("retry");
  });

  it("retries on 408 and 429", () => {
    expect(classify({ status: 408 })).toBe("retry");
    expect(classify({ status: 429 })).toBe("retry");
  });

  it("retries on 5xx", () => {
    for (const s of [500, 502, 503, 504]) {
      expect(classify({ status: s })).toBe("retry");
    }
  });

  it("fails fast on most 4xx", () => {
    for (const s of [400, 401, 403, 404, 413]) {
      expect(classify({ status: s })).toBe("fail_fast");
    }
  });

  it("fails fast on AbortError regardless of status", () => {
    expect(classify({ name: "AbortError" })).toBe("fail_fast");
    expect(classify({ name: "AbortError", status: 500 })).toBe("fail_fast");
  });
});

describe("shouldRetry", () => {
  it("returns false once attempts hit MAX_ATTEMPTS", () => {
    expect(shouldRetry({ status: 503 }, MAX_ATTEMPTS)).toBe(false);
    expect(shouldRetry({ status: 503 }, MAX_ATTEMPTS - 1)).toBe(true);
  });

  it("respects the classifier", () => {
    expect(shouldRetry({ status: 403 }, 1)).toBe(false);
    expect(shouldRetry({ status: 503 }, 1)).toBe(true);
  });
});

describe("backoffDelayMs", () => {
  it("scales exponentially with attempt within bounds", () => {
    // rng=1 => max possible delay for that attempt
    const d1 = backoffDelayMs(1, () => 0.999);
    const d2 = backoffDelayMs(2, () => 0.999);
    const d3 = backoffDelayMs(3, () => 0.999);
    expect(d1).toBeLessThan(d2);
    expect(d2).toBeLessThan(d3);
  });

  it("returns 0 with rng=0 (full jitter low end)", () => {
    expect(backoffDelayMs(5, () => 0)).toBe(0);
  });

  it("clamps at the cap", () => {
    const d = backoffDelayMs(20, () => 0.999);
    expect(d).toBeLessThanOrEqual(30_000);
  });
});
