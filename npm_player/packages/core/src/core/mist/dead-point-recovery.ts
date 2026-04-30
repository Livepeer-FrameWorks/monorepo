export interface DeadPointPauseLike {
  reason?: string;
  begin?: number;
}

export type DeadPointRecoveryDecision =
  | { kind: "noop" }
  | { kind: "pause_only" }
  | { kind: "seek_recover"; seekToMs: number; resetSpeedToAuto: boolean };

export function decideDeadPointRecovery(
  pause: DeadPointPauseLike,
  currentPlayRate: number | "auto" | "fast-forward" | undefined
): DeadPointRecoveryDecision {
  if (pause.reason !== "at_dead_point") {
    return { kind: "noop" };
  }

  if (!Number.isFinite(pause.begin)) {
    return { kind: "pause_only" };
  }

  const isSlowed = typeof currentPlayRate === "number" && currentPlayRate < 1;
  const seekToMs = (pause.begin as number) + (isSlowed ? 1000 : 5000);
  if (!Number.isFinite(seekToMs) || seekToMs <= 0) {
    return { kind: "pause_only" };
  }

  return {
    kind: "seek_recover",
    seekToMs,
    resetSpeedToAuto: isSlowed,
  };
}
