import type { BufferProbe, BufferProbeSample } from "../../core/delivery/buffer-probe";
import type { MistPlayRate } from "../../core/mist/protocol";

export interface FrameTimingProbeSource {
  decoded: number;
  out: number;
  serverCurrentMs?: number;
  serverEndMs?: number;
  jitterMs?: number;
  playRateCurr?: MistPlayRate;
}

export class FrameTimingBufferProbe implements BufferProbe {
  constructor(private readonly getTiming: () => FrameTimingProbeSource | null | undefined) {}

  sample(): BufferProbeSample {
    const timing = this.getTiming();
    if (!timing) return { currentMs: 0 };

    return {
      currentMs: Math.max(0, Math.round(timing.decoded * 1e-3 - timing.out * 1e-3)),
      serverCurrentMs: timing.serverCurrentMs,
      serverEndMs: timing.serverEndMs,
      jitterMs: timing.jitterMs,
      playRateCurr: timing.playRateCurr,
    };
  }
}
