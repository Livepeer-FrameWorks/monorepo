import { describe, it, expect, vi } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import { PlayerProvider } from "../src/context/PlayerContext";
import { usePlayerContext, usePlayerContextOptional } from "../src/context/player";

// Mock player-core
vi.mock("@livepeer-frameworks/player-core", () => ({
  PlayerController: vi.fn().mockImplementation(() => ({
    attach: vi.fn().mockResolvedValue(undefined),
    destroy: vi.fn(),
    on: vi.fn().mockReturnValue(() => {}),
    isPlaying: vi.fn().mockReturnValue(false),
    isPaused: vi.fn().mockReturnValue(true),
    isBuffering: vi.fn().mockReturnValue(false),
    isMuted: vi.fn().mockReturnValue(true),
    getVolume: vi.fn().mockReturnValue(1),
    hasPlaybackStarted: vi.fn().mockReturnValue(false),
    shouldShowControls: vi.fn().mockReturnValue(false),
    shouldShowIdleScreen: vi.fn().mockReturnValue(true),
    getPlaybackQuality: vi.fn().mockReturnValue(null),
    isLoopEnabled: vi.fn().mockReturnValue(false),
    isSubtitlesEnabled: vi.fn().mockReturnValue(false),
    getStreamInfo: vi.fn().mockReturnValue(null),
  })),
  cn: (...args: string[]) => args.filter(Boolean).join(" "),
}));

describe("PlayerContext", () => {
  it("usePlayerContext throws when used outside provider", () => {
    const ConsumerComponent = () => {
      usePlayerContext();
      return <div>should not render</div>;
    };

    // Suppress console.error for expected error
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});

    expect(() => {
      render(<ConsumerComponent />);
    }).toThrow("usePlayerContext must be used within a PlayerProvider");

    spy.mockRestore();
  });

  it("usePlayerContextOptional returns null outside provider", () => {
    let contextValue: unknown = "not-set";
    const ConsumerComponent = () => {
      contextValue = usePlayerContextOptional();
      return <div data-testid="consumer">rendered</div>;
    };

    render(<ConsumerComponent />);

    expect(contextValue).toBeNull();
    expect(screen.getByTestId("consumer")).toBeTruthy();
  });

  it("PlayerProvider renders children", () => {
    render(
      <PlayerProvider config={{ contentId: "test", contentType: "live" }}>
        <div data-testid="child">Hello</div>
      </PlayerProvider>
    );

    expect(screen.getByTestId("child")).toBeTruthy();
    expect(screen.getByText("Hello")).toBeTruthy();
  });

  it("usePlayerContext returns controller state within provider", () => {
    let contextValue: unknown = null;
    const ConsumerComponent = () => {
      contextValue = usePlayerContext();
      return <div data-testid="consumer">got context</div>;
    };

    render(
      <PlayerProvider config={{ contentId: "test", contentType: "live" }}>
        <ConsumerComponent />
      </PlayerProvider>
    );

    expect(contextValue).not.toBeNull();
    const ctx = contextValue as Record<string, unknown>;
    expect(ctx.containerRef).toBeDefined();
    expect(ctx.state).toBeDefined();
    expect(typeof ctx.play).toBe("function");
    expect(typeof ctx.pause).toBe("function");
    expect(typeof ctx.togglePlay).toBe("function");
  });
});
