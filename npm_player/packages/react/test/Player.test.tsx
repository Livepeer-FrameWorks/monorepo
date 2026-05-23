import { describe, expect, it, beforeEach, vi } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import Player from "../src/components/Player";

const mocks = vi.hoisted(() => ({
  usePlayerController: vi.fn(),
}));

vi.mock("../src/hooks/usePlayerController", () => ({
  usePlayerController: mocks.usePlayerController,
}));

vi.mock("../src/components/PlayerControls", () => ({
  default: () => <div data-testid="player-controls" />,
}));

function makePlayerHook() {
  const video = document.createElement("video");
  return {
    containerRef: { current: null },
    state: {
      state: "ready",
      streamState: null,
      endpoints: null,
      metadata: null,
      videoElement: video,
      currentTime: 0,
      duration: 0,
      isPlaying: false,
      isPaused: true,
      isBuffering: false,
      isMuted: true,
      volume: 1,
      error: null,
      errorDetails: null,
      isPassiveError: false,
      hasPlaybackStarted: true,
      isHoldingSpeed: false,
      holdSpeed: 2,
      isHovering: true,
      shouldShowControls: true,
      isLoopEnabled: false,
      isFullscreen: false,
      isPiPActive: false,
      isEffectivelyLive: true,
      shouldShowIdleScreen: false,
      currentPlayerInfo: null,
      currentSourceInfo: null,
      playbackQuality: null,
      subtitlesEnabled: false,
      qualities: [],
      textTracks: [],
      streamInfo: null,
      toast: null,
      thumbnailCues: [],
      loadingPoster: null,
      shouldShowLoadingPoster: false,
    },
    controller: {
      canAttemptFallback: () => false,
      retryWithFallback: vi.fn(),
    },
    play: vi.fn(),
    pause: vi.fn(),
    togglePlay: vi.fn(),
    seek: vi.fn(),
    seekBy: vi.fn(),
    jumpToLive: vi.fn(),
    setVolume: vi.fn(),
    toggleMute: vi.fn(),
    toggleLoop: vi.fn(),
    toggleFullscreen: vi.fn(),
    togglePiP: vi.fn(),
    toggleSubtitles: vi.fn(),
    clearError: vi.fn(),
    dismissToast: vi.fn(),
    retry: vi.fn(),
    reload: vi.fn(),
    getQualities: vi.fn(() => []),
    selectQuality: vi.fn(),
    handleMouseEnter: vi.fn(),
    handleMouseLeave: vi.fn(),
    handleMouseMove: vi.fn(),
    handleTouchStart: vi.fn(),
    setDevModeOptions: vi.fn(),
  };
}

describe("Player", () => {
  beforeEach(() => {
    mocks.usePlayerController.mockReturnValue(makePlayerHook());
  });

  it("does not render wrapper controls when controls are disabled", () => {
    render(<Player contentId="stream-1" contentType="live" options={{ controls: false }} />);

    expect(screen.queryByTestId("player-controls")).toBeNull();
    expect(mocks.usePlayerController).toHaveBeenCalledWith(
      expect.objectContaining({ controls: false })
    );
  });

  it("renders wrapper controls by default while keeping native controls disabled", () => {
    render(<Player contentId="stream-1" contentType="live" />);

    expect(screen.getByTestId("player-controls")).toBeTruthy();
    expect(mocks.usePlayerController).toHaveBeenCalledWith(
      expect.objectContaining({ controls: false })
    );
  });

  it("uses native controls only when stockControls is enabled", () => {
    render(<Player contentId="stream-1" contentType="live" options={{ stockControls: true }} />);

    expect(screen.queryByTestId("player-controls")).toBeNull();
    expect(mocks.usePlayerController).toHaveBeenCalledWith(
      expect.objectContaining({ controls: true })
    );
  });
});
