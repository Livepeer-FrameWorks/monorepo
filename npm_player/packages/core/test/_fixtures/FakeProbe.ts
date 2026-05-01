import type { BufferProbe, BufferProbeSample } from "../../src/core/delivery/buffer-probe";

export class FakeProbe implements BufferProbe {
  current: BufferProbeSample = { currentMs: 0 };

  sample(): BufferProbeSample {
    return this.current;
  }
}
