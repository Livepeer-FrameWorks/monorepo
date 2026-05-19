export type AutoplayResult = "success" | "muted" | "failed";
export type AutoplayStage = "initial" | "muted";

export interface AutoplayAttemptFailure {
  stage: AutoplayStage;
  error: unknown;
}

/**
 * Upstream-parity autoplay recovery: unmuted → muted retry → give up.
 * Returns the outcome so callers can wire telemetry / show play affordance.
 */
export async function attemptAutoplay(
  video: HTMLVideoElement,
  callbacks?: {
    play?: () => Promise<void> | void;
    onAttemptFailed?: (failure: AutoplayAttemptFailure) => void;
    onMutedFallback?: () => void;
    onFailed?: (failure: AutoplayAttemptFailure) => void;
  }
): Promise<AutoplayResult> {
  const play = callbacks?.play ?? (() => video.play());
  try {
    await play();
    return "success";
  } catch (error) {
    callbacks?.onAttemptFailed?.({ stage: "initial", error });
    // Stage 2: retry muted
    const wasMuted = video.muted;
    video.muted = true;
    try {
      await play();
      callbacks?.onMutedFallback?.();
      return "muted";
    } catch (mutedError) {
      // Restore mute state and pause to halt server download
      video.muted = wasMuted;
      video.pause();
      const failure = { stage: "muted" as const, error: mutedError };
      callbacks?.onAttemptFailed?.(failure);
      callbacks?.onFailed?.(failure);
      return "failed";
    }
  }
}
