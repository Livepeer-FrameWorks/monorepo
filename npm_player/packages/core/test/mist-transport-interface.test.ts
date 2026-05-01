import { describe, expectTypeOf, it } from "vitest";
import type {
  MistControlTransport,
  MistMediaTransport,
  MistMetadataTransport,
  MistSendDecorator,
  MistSendListener,
} from "../src/core/mist/transport";
import type { MistCommand, MistMetadataCommand } from "../src/core/mist/protocol";

describe("MistControlTransport interface", () => {
  it("exposes typed send/decorator/listener contracts", () => {
    expectTypeOf<MistSendDecorator<MistCommand>>().toBeFunction();
    expectTypeOf<MistSendListener<MistCommand>>().toBeFunction();
    expectTypeOf<MistControlTransport<MistCommand>["send"]>().toBeFunction();
    expectTypeOf<MistMediaTransport["send"]>().toBeFunction();
    expectTypeOf<MistMetadataTransport["send"]>().toBeFunction();
    expectTypeOf<MistControlTransport<MistMetadataCommand>["send"]>().toBeFunction();
  });
});
