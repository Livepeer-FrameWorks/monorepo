import type { BufferProbe, BufferProbeSample } from "../../core/delivery/buffer-probe";
import type { MistPlayRate } from "../../core/mist/protocol";

export class MewsBufferedProbe implements BufferProbe {
  private serverCurrentMs: number | undefined;
  private serverEndMs: number | undefined;
  private jitterMs: number | undefined;
  private playRateCurr: MistPlayRate | undefined;

  constructor(private readonly video: HTMLVideoElement) {}

  updateServerState(state: {
    currentMs?: number;
    endMs?: number;
    jitterMs?: number;
    playRateCurr?: MistPlayRate;
  }): void {
    this.serverCurrentMs = state.currentMs;
    this.serverEndMs = state.endMs;
    this.jitterMs = state.jitterMs;
    this.playRateCurr = state.playRateCurr;
  }

  sample(): BufferProbeSample {
    const currentMs = this.measureForwardBufferMs();
    return {
      currentMs,
      serverCurrentMs: this.serverCurrentMs,
      serverEndMs: this.serverEndMs,
      jitterMs: this.jitterMs,
      playRateCurr: this.playRateCurr,
    };
  }

  private measureForwardBufferMs(): number {
    const buffered = this.video.buffered;
    const current = this.video.currentTime;
    for (let i = 0; i < buffered.length; i++) {
      if (buffered.start(i) <= current && buffered.end(i) >= current) {
        return Math.max(0, Math.round((buffered.end(i) - current) * 1000));
      }
    }
    return 0;
  }
}
