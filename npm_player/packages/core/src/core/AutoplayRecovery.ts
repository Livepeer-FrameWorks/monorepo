export type AutoplayResult = "success" | "muted" | "failed";

/**
 * Upstream-parity autoplay recovery: unmuted → muted retry → give up.
 * Returns the outcome so callers can wire telemetry / show play affordance.
 */
export async function attemptAutoplay(
  video: HTMLVideoElement,
  callbacks?: {
    onMutedFallback?: () => void;
    onFailed?: () => void;
  }
): Promise<AutoplayResult> {
  try {
    await video.play();
    return "success";
  } catch {
    // Stage 2: retry muted
    const wasMuted = video.muted;
    video.muted = true;
    try {
      await video.play();
      callbacks?.onMutedFallback?.();
      return "muted";
    } catch {
      // Restore mute state and pause to halt server download
      video.muted = wasMuted;
      video.pause();
      callbacks?.onFailed?.();
      return "failed";
    }
  }
}
