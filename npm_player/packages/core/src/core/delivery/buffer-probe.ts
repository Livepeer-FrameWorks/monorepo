import type { MistPlayRate } from "../mist/protocol";

export interface BufferProbeSample {
  currentMs: number;
  targetMs?: number;
  serverCurrentMs?: number;
  serverEndMs?: number;
  jitterMs?: number;
  playRateCurr?: MistPlayRate;
}

export interface BufferProbe {
  sample(): BufferProbeSample;
}

export const NULL_PROBE: BufferProbe = {
  sample: () => ({ currentMs: 0 }),
};

export function fixedBufferProbe(sample: BufferProbeSample): BufferProbe {
  return { sample: () => sample };
}
