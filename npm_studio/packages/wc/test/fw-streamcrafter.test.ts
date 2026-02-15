import { describe, it, expect } from "vitest";
import { FwStreamCrafter } from "../src/components/fw-streamcrafter.js";

describe("FwStreamCrafter", () => {
  it("is a class that extends HTMLElement", () => {
    expect(FwStreamCrafter).toBeDefined();
    expect(FwStreamCrafter.prototype instanceof HTMLElement).toBe(true);
  });

  it("has the expected public API methods", () => {
    const proto = FwStreamCrafter.prototype;
    expect(typeof proto.startCamera).toBe("function");
    expect(typeof proto.startScreenShare).toBe("function");
    expect(typeof proto.startStreaming).toBe("function");
    expect(typeof proto.stopStreaming).toBe("function");
    expect(typeof proto.stopCapture).toBe("function");
    expect(typeof proto.removeSource).toBe("function");
    expect(typeof proto.setSourceVolume).toBe("function");
    expect(typeof proto.setSourceMuted).toBe("function");
    expect(typeof proto.setPrimaryVideoSource).toBe("function");
    expect(typeof proto.setMasterVolume).toBe("function");
    expect(typeof proto.setQualityProfile).toBe("function");
    expect(typeof proto.destroy).toBe("function");
  });
});
