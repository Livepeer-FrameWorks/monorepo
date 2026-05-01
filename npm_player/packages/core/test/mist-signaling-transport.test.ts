import { describe, expectTypeOf, it } from "vitest";
import { MistSignalingTransport } from "../src/core/mist/transports/signaling-transport";
import type { MistMediaTransport } from "../src/core/mist/transport";

describe("MistSignalingTransport", () => {
  it("implements MistMediaTransport contract", () => {
    expectTypeOf<MistSignalingTransport>().toMatchTypeOf<MistMediaTransport>();
  });
});
