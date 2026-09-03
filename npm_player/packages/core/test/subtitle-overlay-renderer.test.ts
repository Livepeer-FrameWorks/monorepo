import { afterEach, describe, expect, it, vi } from "vitest";

import {
  SubtitleOverlayRenderer,
  subtitleCueDurationMs,
} from "../src/core/SubtitleOverlayRenderer";

describe("SubtitleOverlayRenderer", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("uses cue end time before the four-second fallback", () => {
    expect(
      subtitleCueDurationMs({
        type: "subtitle",
        trackId: "7",
        timestamp: 1_000,
        data: { text: "Hello", endTime: 1_750 },
      })
    ).toBe(750);
  });

  it("uses the fallback for null cue timing", () => {
    expect(
      subtitleCueDurationMs({
        type: "subtitle",
        trackId: "7",
        timestamp: 1_000,
        data: { text: "Hello", endTime: null, durationMs: null },
      })
    ).toBe(4_000);
  });

  it("renders text safely and clears it at the cue end", () => {
    vi.useFakeTimers();
    const overlay = { className: "", style: {}, textContent: "", remove: vi.fn() };
    vi.stubGlobal("document", { createElement: vi.fn(() => overlay) });
    const container = { appendChild: vi.fn() } as unknown as HTMLElement;
    const renderer = new SubtitleOverlayRenderer(container);

    renderer.render({
      type: "subtitle",
      trackId: "7",
      timestamp: 1_000,
      data: { text: "<b>literal</b>", endTime: 1_750 },
    });

    expect(overlay.textContent).toBe("<b>literal</b>");
    vi.advanceTimersByTime(749);
    expect(overlay.textContent).toBe("<b>literal</b>");
    vi.advanceTimersByTime(1);
    expect(overlay.textContent).toBe("");
    renderer.destroy();
  });

  it("clears a visible cue when an empty end cue arrives", () => {
    vi.useFakeTimers();
    const overlay = { className: "", style: {}, textContent: "", remove: vi.fn() };
    vi.stubGlobal("document", { createElement: vi.fn(() => overlay) });
    const renderer = new SubtitleOverlayRenderer({
      appendChild: vi.fn(),
    } as unknown as HTMLElement);

    renderer.render({ type: "subtitle", trackId: "7", timestamp: 1_000, data: "Hello" });
    renderer.render({ type: "subtitle", trackId: "7", timestamp: 2_000, data: "" });

    expect(overlay.textContent).toBe("");
    renderer.destroy();
  });

  it("uses playback time so pause preserves a cue and seek clears it", () => {
    vi.useFakeTimers();
    let playbackTime = 1_000;
    const overlay = { className: "", style: {}, textContent: "", remove: vi.fn() };
    vi.stubGlobal("document", { createElement: vi.fn(() => overlay) });
    const renderer = new SubtitleOverlayRenderer(
      { appendChild: vi.fn() } as unknown as HTMLElement,
      { getPlaybackTimeMs: () => playbackTime }
    );

    renderer.render({
      type: "subtitle",
      trackId: "7",
      timestamp: 1_000,
      data: { text: "Held while paused", endTime: 1_750 },
    });
    vi.advanceTimersByTime(2_000);
    expect(overlay.textContent).toBe("Held while paused");

    playbackTime = 500;
    vi.advanceTimersByTime(100);
    expect(overlay.textContent).toBe("");
    renderer.destroy();
  });
});
