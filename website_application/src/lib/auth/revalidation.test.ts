import { afterEach, describe, expect, it, vi } from "vitest";

import { createTrailingRevalidator } from "./revalidation";

afterEach(() => {
  vi.useRealTimers();
});

describe("createTrailingRevalidator", () => {
  it("coalesces focus and visibility notifications", () => {
    vi.useFakeTimers();
    const revalidate = vi.fn();
    const coordinator = createTrailingRevalidator(revalidate, 250);

    coordinator.schedule();
    vi.advanceTimersByTime(100);
    coordinator.schedule();
    vi.advanceTimersByTime(249);
    expect(revalidate).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(revalidate).toHaveBeenCalledTimes(1);
  });

  it("cancels pending work on teardown", () => {
    vi.useFakeTimers();
    const revalidate = vi.fn();
    const coordinator = createTrailingRevalidator(revalidate);
    coordinator.schedule();
    coordinator.cancel();
    vi.runAllTimers();
    expect(revalidate).not.toHaveBeenCalled();
  });
});
