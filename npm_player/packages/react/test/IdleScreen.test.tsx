import { describe, expect, it, vi } from "vitest";
import React from "react";
import { act, render, screen, cleanup } from "@testing-library/react";
import IdleScreen from "../src/components/IdleScreen";

describe("IdleScreen", () => {
  it("cancels every stage of the idle animation on unmount", () => {
    cleanup();
    vi.useFakeTimers();
    try {
      for (const elapsed of [1, 4500, 8000]) {
        const baseline = vi.getTimerCount();
        const view = render(<IdleScreen status="ERROR" error="HTTP 503" />);
        act(() => vi.advanceTimersByTime(elapsed));
        view.unmount();
        expect(vi.getTimerCount()).toBe(baseline);
        act(() => vi.advanceTimersByTime(20000));
        expect(vi.getTimerCount()).toBe(baseline);
      }
    } finally {
      cleanup();
      vi.clearAllTimers();
      vi.useRealTimers();
    }
  });
  it("shows raw error text instead of only generic stream status", () => {
    render(<IdleScreen status="ERROR" message="Stream error" error="HTTP 503" />);

    expect(screen.getByText("HTTP 503")).toBeTruthy();
    expect(screen.queryByText("Stream error")).toBeNull();
  });

  it("shows diagnostic details when supplied separately", () => {
    render(
      <IdleScreen
        status="ERROR"
        message="Connection failed"
        error="HTTP 503"
        details="GET /json_stream.js returned 503"
      />
    );

    expect(screen.getByText("HTTP 503")).toBeTruthy();
    expect(screen.getByText("GET /json_stream.js returned 503")).toBeTruthy();
  });
});
