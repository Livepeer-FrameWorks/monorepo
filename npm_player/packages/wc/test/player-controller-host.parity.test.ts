import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactiveControllerHost } from "lit";
import { WRAPPER_PARITY_EVENT_NAMES } from "../../test-contract/player-wrapper-contract";

let eventHandlers: Map<string, Function>;

const mockAttach = vi.fn().mockResolvedValue(undefined);
const mockDestroy = vi.fn();
const mockOn = vi.fn((event: string, handler: Function) => {
  eventHandlers.set(event, handler);
  return () => eventHandlers.delete(event);
});
const mockIsLoopEnabled = vi.fn().mockReturnValue(false);

vi.mock("@livepeer-frameworks/player-core", () => ({
  PlayerController: vi.fn().mockImplementation(function (this: Record<string, unknown>) {
    Object.assign(this, {
      attach: mockAttach,
      destroy: mockDestroy,
      on: mockOn,
      isLoopEnabled: mockIsLoopEnabled,
    });
  }),
}));

import { PlayerControllerHost } from "../src/controllers/player-controller-host.js";

function createMockHost(): ReactiveControllerHost & HTMLElement {
  const host = document.createElement("div") as unknown as ReactiveControllerHost & HTMLElement;
  (host as any).addController = vi.fn();
  (host as any).requestUpdate = vi.fn();
  (host as any).removeController = vi.fn();
  (host as any).updateComplete = Promise.resolve(true);
  return host;
}

describe("PlayerControllerHost parity", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    eventHandlers = new Map();
  });

  it("subscribes to the shared wrapper event set", async () => {
    const host = createMockHost();
    const pc = new PlayerControllerHost(host);
    pc.configure({ contentId: "test", contentType: "live" });

    await pc.attach(document.createElement("div"));

    const eventNames = mockOn.mock.calls.map((call: unknown[]) => call[0]);
    for (const eventName of WRAPPER_PARITY_EVENT_NAMES) {
      expect(eventNames).toContain(eventName);
    }
  });
});
