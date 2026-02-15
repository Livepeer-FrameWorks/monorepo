import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactiveControllerHost } from "lit";
import { STREAMCRAFTER_WRAPPER_PARITY_EVENT_NAMES } from "../../test-contract/streamcrafter-wrapper-contract";

let eventHandlers: Map<string, Function>;

const mockDestroy = vi.fn();
const mockOn = vi.fn((event: string, handler: Function) => {
  eventHandlers.set(event, handler);
  return () => eventHandlers.delete(event);
});

vi.mock("@livepeer-frameworks/streamcrafter-core", () => ({
  IngestControllerV2: vi.fn().mockImplementation(function (this: Record<string, unknown>) {
    Object.assign(this, {
      destroy: mockDestroy,
      on: mockOn,
    });
  }),
  detectCapabilities: vi.fn().mockReturnValue({ recommended: "mediastream" }),
  isWebCodecsEncodingPathSupported: vi.fn().mockReturnValue(false),
}));

import { IngestControllerHost } from "../src/controllers/ingest-controller-host.js";

function createMockHost(): ReactiveControllerHost & HTMLElement {
  const host = document.createElement("div") as unknown as ReactiveControllerHost & HTMLElement;
  (host as any).addController = vi.fn();
  (host as any).requestUpdate = vi.fn();
  (host as any).removeController = vi.fn();
  (host as any).updateComplete = Promise.resolve(true);
  return host;
}

describe("IngestControllerHost parity", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    eventHandlers = new Map();
  });

  it("subscribes to the shared wrapper event set", () => {
    const host = createMockHost();
    const ic = new IngestControllerHost(host);
    ic.initialize({ whipUrl: "https://example.com/whip" });

    const eventNames = mockOn.mock.calls.map((call: unknown[]) => call[0]);
    for (const eventName of STREAMCRAFTER_WRAPPER_PARITY_EVENT_NAMES) {
      expect(eventNames).toContain(eventName);
    }
  });
});
