import { describe, expect, it } from "vitest";

import { WhipClient } from "../src/core/WhipClient";

// Minimal unit tests for canUseEncodedInsertion()
// We don't attempt real WebRTC/WebCodecs; we only validate the gatekeeping logic.

describe("WhipClient.canUseEncodedInsertion", () => {
  it("returns false when not connected", () => {
    const client = new WhipClient({ endpoint: "https://example.com", debug: false } as any);
    expect(client.canUseEncodedInsertion()).toBe(false);
  });

  it("returns true when connected + transform is supported", () => {
    // Provide RTCRtpScriptTransform global
    (globalThis as any).RTCRtpScriptTransform = function () {};

    const client = new WhipClient({ endpoint: "https://example.com", debug: false } as any);

    // Force internal state for unit test
    (client as any).state = "connected";
    (client as any).peerConnection = {
      getSenders() {
        return [{ transform: {} }];
      },
    };

    expect(client.canUseEncodedInsertion()).toBe(true);
  });

  it("returns false when senders do not have transform support", () => {
    (globalThis as any).RTCRtpScriptTransform = function () {};

    const client = new WhipClient({ endpoint: "https://example.com", debug: false } as any);
    (client as any).state = "connected";
    (client as any).peerConnection = {
      getSenders() {
        return [{}];
      },
    };

    expect(client.canUseEncodedInsertion()).toBe(false);
  });
});
