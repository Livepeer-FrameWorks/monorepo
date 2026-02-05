import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { ScreenWakeLockManager } from "../src/core/ScreenWakeLockManager";

// Minimal WakeLockSentinel stub
function createSentinel() {
  const handlers = new Map<string, Function>();
  return {
    released: false,
    type: "screen" as const,
    release: vi.fn(async () => {}),
    addEventListener: vi.fn((event: string, handler: Function) => {
      handlers.set(event, handler);
    }),
    removeEventListener: vi.fn(),
    onrelease: null,
    dispatchEvent: vi.fn(() => true),
    _fireRelease() {
      const h = handlers.get("release");
      if (h) h();
    },
  };
}

describe("ScreenWakeLockManager", () => {
  let sentinel: ReturnType<typeof createSentinel>;
  let origDocument: typeof globalThis.document;
  let origNavigatorDescriptor: PropertyDescriptor | undefined;
  const docListeners = new Map<string, Function>();

  function stubWakeLock() {
    sentinel = createSentinel();
    Object.defineProperty(globalThis.navigator, "wakeLock", {
      value: { request: vi.fn(async () => sentinel) },
      configurable: true,
      writable: true,
    });
  }

  function removeWakeLock() {
    try {
      // @ts-expect-error - remove optional prop
      delete globalThis.navigator.wakeLock;
    } catch {
      Object.defineProperty(globalThis.navigator, "wakeLock", {
        value: undefined,
        configurable: true,
        writable: true,
      });
    }
  }

  beforeEach(() => {
    origNavigatorDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, "wakeLock");
    origDocument = globalThis.document;
    docListeners.clear();

    // Stub document globally since we're in Node environment
    (globalThis as any).document = {
      addEventListener: vi.fn((event: string, handler: Function) => {
        docListeners.set(event, handler);
      }),
      removeEventListener: vi.fn((event: string, _handler: Function) => {
        docListeners.delete(event);
      }),
      visibilityState: "visible",
    };
  });

  afterEach(() => {
    if (origNavigatorDescriptor) {
      Object.defineProperty(globalThis.navigator, "wakeLock", origNavigatorDescriptor);
    } else {
      removeWakeLock();
    }
    (globalThis as any).document = origDocument;
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Static isSupported
  // ===========================================================================
  describe("isSupported", () => {
    it("returns true when navigator.wakeLock exists", () => {
      stubWakeLock();
      expect(ScreenWakeLockManager.isSupported()).toBe(true);
    });

    it("returns false when navigator.wakeLock missing", () => {
      removeWakeLock();
      expect(ScreenWakeLockManager.isSupported()).toBe(false);
    });
  });

  // ===========================================================================
  // State machine: setPlaying + setFullscreen
  // ===========================================================================
  describe("state machine", () => {
    it("does not acquire on play alone (default config)", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      mgr.setPlaying(true);
      await vi.waitFor(() => {});
      expect(mgr.isHeld()).toBe(false);
      mgr.destroy();
    });

    it("acquires when playing + fullscreen", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      mgr.setPlaying(true);
      mgr.setFullscreen(true);
      await vi.waitFor(() => expect(mgr.isHeld()).toBe(true));
      mgr.destroy();
    });

    it("acquires on play when acquireOnPlay is true", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager({ acquireOnPlay: true });
      mgr.setPlaying(true);
      await vi.waitFor(() => expect(mgr.isHeld()).toBe(true));
      mgr.destroy();
    });

    it("releases when playing stops", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager({ acquireOnPlay: true });
      mgr.setPlaying(true);
      await vi.waitFor(() => expect(mgr.isHeld()).toBe(true));

      mgr.setPlaying(false);
      expect(mgr.isHeld()).toBe(false);
      mgr.destroy();
    });

    it("releases when fullscreen exits (non-acquireOnPlay)", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      mgr.setPlaying(true);
      mgr.setFullscreen(true);
      await vi.waitFor(() => expect(mgr.isHeld()).toBe(true));

      mgr.setFullscreen(false);
      expect(mgr.isHeld()).toBe(false);
      mgr.destroy();
    });

    it("does nothing after destroy", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager({ acquireOnPlay: true });
      mgr.destroy();

      mgr.setPlaying(true);
      await vi.waitFor(() => {});
      expect(mgr.isHeld()).toBe(false);
    });
  });

  // ===========================================================================
  // acquire / release
  // ===========================================================================
  describe("acquire / release", () => {
    it("acquire calls navigator.wakeLock.request", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      await mgr.acquire();
      expect((globalThis.navigator as any).wakeLock.request).toHaveBeenCalledWith("screen");
      expect(mgr.isHeld()).toBe(true);
      mgr.destroy();
    });

    it("double acquire is a no-op", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      await mgr.acquire();
      await mgr.acquire();
      expect((globalThis.navigator as any).wakeLock.request).toHaveBeenCalledTimes(1);
      mgr.destroy();
    });

    it("acquire on unsupported browser is a no-op", async () => {
      removeWakeLock();
      const mgr = new ScreenWakeLockManager();
      await mgr.acquire();
      expect(mgr.isHeld()).toBe(false);
      mgr.destroy();
    });

    it("acquire error calls onError callback", async () => {
      stubWakeLock();
      const err = new Error("Low battery");
      (globalThis.navigator as any).wakeLock.request = vi.fn(async () => {
        throw err;
      });
      const onError = vi.fn();
      const mgr = new ScreenWakeLockManager({ onError });
      await mgr.acquire();
      expect(onError).toHaveBeenCalledWith(err);
      expect(mgr.isHeld()).toBe(false);
      mgr.destroy();
    });

    it("release calls sentinel.release()", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      await mgr.acquire();
      mgr.release();
      expect(sentinel.release).toHaveBeenCalled();
      expect(mgr.isHeld()).toBe(false);
      mgr.destroy();
    });

    it("release when not held is a no-op", () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      expect(() => mgr.release()).not.toThrow();
      mgr.destroy();
    });
  });

  // ===========================================================================
  // Callbacks
  // ===========================================================================
  describe("callbacks", () => {
    it("onAcquire fires on successful acquire", async () => {
      stubWakeLock();
      const onAcquire = vi.fn();
      const mgr = new ScreenWakeLockManager({ onAcquire });
      await mgr.acquire();
      expect(onAcquire).toHaveBeenCalledTimes(1);
      mgr.destroy();
    });

    it("onRelease fires when sentinel releases", async () => {
      stubWakeLock();
      const onRelease = vi.fn();
      const mgr = new ScreenWakeLockManager({ onRelease });
      await mgr.acquire();
      sentinel._fireRelease();
      expect(onRelease).toHaveBeenCalledTimes(1);
      mgr.destroy();
    });
  });

  // ===========================================================================
  // handleRelease (re-acquire)
  // ===========================================================================
  describe("handleRelease", () => {
    it("re-acquires if conditions still met", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager({ acquireOnPlay: true });
      mgr.setPlaying(true);
      await vi.waitFor(() => expect(mgr.isHeld()).toBe(true));

      (globalThis.navigator as any).wakeLock.request.mockClear();
      sentinel._fireRelease();
      await vi.waitFor(() =>
        expect((globalThis.navigator as any).wakeLock.request).toHaveBeenCalledTimes(1)
      );
      mgr.destroy();
    });

    it("does not re-acquire after destroy", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager({ acquireOnPlay: true });
      mgr.setPlaying(true);
      await vi.waitFor(() => expect(mgr.isHeld()).toBe(true));

      mgr.destroy();
      (globalThis.navigator as any).wakeLock.request.mockClear();
      sentinel._fireRelease();
      await vi.waitFor(() => {});
      expect((globalThis.navigator as any).wakeLock.request).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // Visibility change
  // ===========================================================================
  describe("visibility change", () => {
    it("re-acquires when page becomes visible and conditions met", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager({ acquireOnPlay: true });
      mgr.setPlaying(true);
      await vi.waitFor(() => expect(mgr.isHeld()).toBe(true));

      // Simulate release (screen off)
      sentinel._fireRelease();
      expect(mgr.isHeld()).toBe(false);

      // Simulate page visible
      (globalThis as any).document.visibilityState = "visible";
      const handler = docListeners.get("visibilitychange");
      if (handler) handler();

      await vi.waitFor(() => expect(mgr.isHeld()).toBe(true));
      mgr.destroy();
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("releases wake lock and removes event listener", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      await mgr.acquire();
      mgr.destroy();
      expect(mgr.isHeld()).toBe(false);
      expect((globalThis as any).document.removeEventListener).toHaveBeenCalledWith(
        "visibilitychange",
        expect.any(Function)
      );
    });

    it("double destroy is a no-op", async () => {
      stubWakeLock();
      const mgr = new ScreenWakeLockManager();
      await mgr.acquire();
      mgr.destroy();
      expect(() => mgr.destroy()).not.toThrow();
    });
  });
});
