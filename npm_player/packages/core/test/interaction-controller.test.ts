import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  InteractionController,
  type InteractionControllerConfig,
} from "../src/core/InteractionController";

// ---------------------------------------------------------------------------
// DOM stub helpers
// ---------------------------------------------------------------------------

function createMockElement(tag = "div") {
  const listeners = new Map<string, Function[]>();
  return {
    tagName: tag.toUpperCase(),
    hasAttribute: vi.fn(() => false),
    setAttribute: vi.fn(),
    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event)!.push(handler);
    }),
    removeEventListener: vi.fn((event: string, handler: Function) => {
      const list = listeners.get(event);
      if (list) {
        const idx = list.indexOf(handler);
        if (idx >= 0) list.splice(idx, 1);
      }
    }),
    contains: vi.fn(() => true),
    matches: vi.fn(() => false),
    closest: vi.fn(() => null),
    focus: vi.fn(),
    getBoundingClientRect: vi.fn(() => ({ left: 0, top: 0, width: 300, height: 200 })),
    isContentEditable: false,
    _fire(event: string, detail: Record<string, unknown> = {}) {
      const handlers = listeners.get(event);
      if (!handlers) return;
      const evt = {
        key: "",
        button: 0,
        clientX: 0,
        clientY: 0,
        pointerType: "mouse",
        target: this,
        defaultPrevented: false,
        preventDefault: vi.fn(),
        ...detail,
      };
      handlers.forEach((h) => h(evt));
    },
    _listeners: listeners,
  };
}

function createMockVideo() {
  const el = createMockElement("video");
  return {
    ...el,
    paused: false,
    playbackRate: 1,
    currentTime: 30,
    buffered: {
      length: 1,
      start: () => 0,
      end: () => 60,
    },
  };
}

function makeConfig(overrides: Partial<InteractionControllerConfig> = {}) {
  const container = createMockElement("div");
  const videoElement = createMockVideo();

  return {
    config: {
      container: container as unknown as HTMLElement,
      videoElement: videoElement as unknown as HTMLVideoElement,
      isLive: false,
      isPaused: vi.fn(() => false),
      onPlayPause: vi.fn(),
      onSeek: vi.fn(),
      onVolumeChange: vi.fn(),
      onMuteToggle: vi.fn(),
      onFullscreenToggle: vi.fn(),
      onCaptionsToggle: vi.fn(),
      onLoopToggle: vi.fn(),
      onSpeedChange: vi.fn(),
      onSeekPercent: vi.fn(),
      onFrameStep: vi.fn(),
      onIdle: vi.fn(),
      onActive: vi.fn(),
      idleTimeout: 5000,
      ...overrides,
    } as unknown as InteractionControllerConfig,
    container,
    videoElement,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("InteractionController", () => {
  let origDocument: PropertyDescriptor | undefined;
  let origElement: any;
  let origHTMLElement: any;
  let docListeners: Map<string, Function[]>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(Date, "now").mockReturnValue(10000);
    docListeners = new Map();

    // Stub Element / HTMLElement so `instanceof` works in Node.js
    origElement = (globalThis as any).Element;
    origHTMLElement = (globalThis as any).HTMLElement;
    (globalThis as any).Element = class Element {};
    (globalThis as any).HTMLElement = class HTMLElement extends (globalThis as any).Element {};

    origDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
    Object.defineProperty(globalThis, "document", {
      value: {
        addEventListener: vi.fn((event: string, handler: Function) => {
          if (!docListeners.has(event)) docListeners.set(event, []);
          docListeners.get(event)!.push(handler);
        }),
        removeEventListener: vi.fn(),
        activeElement: null,
      },
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    if (origDocument) {
      Object.defineProperty(globalThis, "document", origDocument);
    }
    if (origHTMLElement !== undefined) {
      (globalThis as any).HTMLElement = origHTMLElement;
    } else {
      delete (globalThis as any).HTMLElement;
    }
    if (origElement !== undefined) {
      (globalThis as any).Element = origElement;
    } else {
      delete (globalThis as any).Element;
    }
  });

  // ===========================================================================
  // Initial state
  // ===========================================================================
  describe("initial state", () => {
    it("starts not holding speed and not idle", () => {
      const { config } = makeConfig();
      const ic = new InteractionController(config);
      expect(ic.isHoldingSpeed()).toBe(false);
      expect(ic.isIdle()).toBe(false);
    });

    it("getState returns copy of state", () => {
      const { config } = makeConfig();
      const ic = new InteractionController(config);
      const s1 = ic.getState();
      const s2 = ic.getState();
      expect(s1).toEqual(s2);
      expect(s1).not.toBe(s2);
    });
  });

  // ===========================================================================
  // Attach / detach
  // ===========================================================================
  describe("attach / detach", () => {
    it("attach sets tabindex and registers events", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      expect(container.setAttribute).toHaveBeenCalledWith("tabindex", "0");
      expect(container.addEventListener).toHaveBeenCalledWith("keydown", expect.any(Function));
      expect(container.addEventListener).toHaveBeenCalledWith("pointerdown", expect.any(Function));
      expect(container.addEventListener).toHaveBeenCalledWith("dblclick", expect.any(Function));
    });

    it("double attach is no-op", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();
      const count = (container.addEventListener as ReturnType<typeof vi.fn>).mock.calls.length;
      ic.attach();
      expect((container.addEventListener as ReturnType<typeof vi.fn>).mock.calls.length).toBe(
        count
      );
    });

    it("detach removes listeners and clears timeouts", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();
      ic.detach();

      expect(container.removeEventListener).toHaveBeenCalledWith("keydown", expect.any(Function));
      expect(container.removeEventListener).toHaveBeenCalledWith(
        "pointerdown",
        expect.any(Function)
      );
    });

    it("detach is no-op when not attached", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.detach();
      expect(container.removeEventListener).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // Keyboard shortcuts
  // ===========================================================================
  describe("keyboard shortcuts", () => {
    it("space tap triggers play/pause", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: " " });
      vi.advanceTimersByTime(50); // < 200ms hold threshold
      container._fire("keyup", { key: " " });

      expect(config.onPlayPause).toHaveBeenCalled();
    });

    it("K triggers play/pause", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "k" });
      expect(config.onPlayPause).toHaveBeenCalled();
    });

    it("ArrowLeft triggers seek backward", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "ArrowLeft" });
      expect(config.onSeek).toHaveBeenCalledWith(-10);
    });

    it("ArrowRight triggers seek forward", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "ArrowRight" });
      expect(config.onSeek).toHaveBeenCalledWith(10);
    });

    it("j/l trigger seek", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "j" });
      expect(config.onSeek).toHaveBeenCalledWith(-10);

      container._fire("keydown", { key: "l" });
      expect(config.onSeek).toHaveBeenCalledWith(10);
    });

    it("ArrowUp/Down changes volume", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "ArrowUp" });
      expect(config.onVolumeChange).toHaveBeenCalledWith(0.1);

      container._fire("keydown", { key: "ArrowDown" });
      expect(config.onVolumeChange).toHaveBeenCalledWith(-0.1);
    });

    it("M toggles mute", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "m" });
      expect(config.onMuteToggle).toHaveBeenCalled();
    });

    it("F toggles fullscreen", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "f" });
      expect(config.onFullscreenToggle).toHaveBeenCalled();
    });

    it("C toggles captions", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "c" });
      expect(config.onCaptionsToggle).toHaveBeenCalled();
    });

    it("number keys seek to percentage", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "5" });
      expect(config.onSeekPercent).toHaveBeenCalledWith(0.5);

      container._fire("keydown", { key: "0" });
      expect(config.onSeekPercent).toHaveBeenCalledWith(0);
    });
  });

  // ===========================================================================
  // Live mode restrictions
  // ===========================================================================
  describe("live mode restrictions", () => {
    it("arrow seek disabled in live mode", () => {
      const { config, container } = makeConfig({ isLive: true });
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "ArrowLeft" });
      container._fire("keydown", { key: "ArrowRight" });
      expect(config.onSeek).not.toHaveBeenCalled();
    });

    it("speed shortcuts disabled in live mode", () => {
      const { config, container } = makeConfig({ isLive: true });
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "<" });
      container._fire("keydown", { key: ">" });
      expect(config.onSpeedChange).not.toHaveBeenCalled();
    });

    it("number key seek disabled in live mode", () => {
      const { config, container } = makeConfig({ isLive: true });
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: "5" });
      expect(config.onSeekPercent).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // Speed hold (space)
  // ===========================================================================
  describe("speed hold", () => {
    it("space hold > 200ms engages speed boost", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: " " });
      vi.advanceTimersByTime(250);

      expect(ic.isHoldingSpeed()).toBe(true);
      expect(config.onSpeedChange).toHaveBeenCalledWith(2, true);

      container._fire("keyup", { key: " " });
      expect(ic.isHoldingSpeed()).toBe(false);
      expect(config.onSpeedChange).toHaveBeenCalledWith(1, false);
    });

    it("space hold disabled in live mode", () => {
      const { config, container } = makeConfig({ isLive: true });
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: " " });
      vi.advanceTimersByTime(500);
      expect(ic.isHoldingSpeed()).toBe(false);
    });

    it("custom speed hold value", () => {
      const { config, container } = makeConfig({ speedHoldValue: 3 } as any);
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: " " });
      vi.advanceTimersByTime(250);

      expect(config.onSpeedChange).toHaveBeenCalledWith(3, true);
      ic.detach();
    });
  });

  // ===========================================================================
  // Idle detection
  // ===========================================================================
  describe("idle detection", () => {
    it("becomes idle after timeout", () => {
      const { config } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      vi.advanceTimersByTime(5000);
      expect(ic.isIdle()).toBe(true);
      expect(config.onIdle).toHaveBeenCalled();
    });

    it("interaction resets idle timer", () => {
      const { config } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      vi.advanceTimersByTime(4000);
      ic.recordInteraction();
      vi.advanceTimersByTime(4000);
      expect(ic.isIdle()).toBe(false);

      vi.advanceTimersByTime(1000);
      expect(ic.isIdle()).toBe(true);
    });

    it("markActive triggers onActive callback", () => {
      const { config } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      vi.advanceTimersByTime(5000);
      expect(ic.isIdle()).toBe(true);

      ic.markActive();
      expect(ic.isIdle()).toBe(false);
      expect(config.onActive).toHaveBeenCalled();
    });

    it("pauseIdleTracking prevents idle", () => {
      const { config } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      ic.pauseIdleTracking();
      vi.advanceTimersByTime(10000);
      expect(ic.isIdle()).toBe(false);
    });

    it("resumeIdleTracking restarts timer", () => {
      const { config } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      ic.pauseIdleTracking();
      ic.resumeIdleTracking();
      vi.advanceTimersByTime(5000);
      expect(ic.isIdle()).toBe(true);
    });

    it("idleTimeout 0 disables idle tracking", () => {
      const { config } = makeConfig({ idleTimeout: 0 });
      const ic = new InteractionController(config);
      ic.attach();

      vi.advanceTimersByTime(60000);
      expect(ic.isIdle()).toBe(false);
    });
  });

  // ===========================================================================
  // updateConfig
  // ===========================================================================
  describe("updateConfig", () => {
    it("switching to live releases speed hold", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("keydown", { key: " " });
      vi.advanceTimersByTime(250);
      expect(ic.isHoldingSpeed()).toBe(true);

      ic.updateConfig({ isLive: true });
      expect(ic.isHoldingSpeed()).toBe(false);
    });
  });

  // ===========================================================================
  // Double click fullscreen
  // ===========================================================================
  describe("double click", () => {
    it("dblclick toggles fullscreen", () => {
      const { config, container } = makeConfig();
      const ic = new InteractionController(config);
      ic.attach();

      container._fire("dblclick", { target: container });
      expect(config.onFullscreenToggle).toHaveBeenCalled();
    });
  });
});
