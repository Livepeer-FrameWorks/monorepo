import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { TimerManager } from "../src/core/TimerManager";

describe("TimerManager", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  // ===========================================================================
  // Constructor
  // ===========================================================================
  describe("constructor", () => {
    it("starts with zero active timers", () => {
      const tm = new TimerManager();
      expect(tm.activeCount).toBe(0);
    });
  });

  // ===========================================================================
  // start (timeout)
  // ===========================================================================
  describe("start", () => {
    it("fires callback after delay", () => {
      const tm = new TimerManager();
      const cb = vi.fn();
      tm.start(cb, 1000);

      expect(cb).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1000);
      expect(cb).toHaveBeenCalledTimes(1);
    });

    it("auto-removes from map after firing", () => {
      const tm = new TimerManager();
      const id = tm.start(() => {}, 500);
      expect(tm.isActive(id)).toBe(true);

      vi.advanceTimersByTime(500);
      expect(tm.isActive(id)).toBe(false);
      expect(tm.activeCount).toBe(0);
    });

    it("returns unique internal IDs", () => {
      const tm = new TimerManager();
      const id1 = tm.start(() => {}, 100);
      const id2 = tm.start(() => {}, 200);
      expect(id1).not.toBe(id2);
    });

    it("isolates callback errors", () => {
      const tm = new TimerManager();
      const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
      tm.start(() => {
        throw new Error("boom");
      }, 100);

      vi.advanceTimersByTime(100);
      expect(errorSpy).toHaveBeenCalledWith("[TimerManager] Callback error:", expect.any(Error));
      expect(tm.activeCount).toBe(0);
      errorSpy.mockRestore();
    });

    it("does not fire before delay", () => {
      const tm = new TimerManager();
      const cb = vi.fn();
      tm.start(cb, 1000);

      vi.advanceTimersByTime(999);
      expect(cb).not.toHaveBeenCalled();
    });

    it("increments activeCount", () => {
      const tm = new TimerManager();
      tm.start(() => {}, 1000);
      tm.start(() => {}, 2000);
      expect(tm.activeCount).toBe(2);
    });
  });

  // ===========================================================================
  // startInterval
  // ===========================================================================
  describe("startInterval", () => {
    it("fires repeatedly", () => {
      const tm = new TimerManager();
      const cb = vi.fn();
      tm.startInterval(cb, 200);

      vi.advanceTimersByTime(1000);
      expect(cb).toHaveBeenCalledTimes(5);
    });

    it("stays active after multiple fires", () => {
      const tm = new TimerManager();
      const id = tm.startInterval(() => {}, 100);
      vi.advanceTimersByTime(500);
      expect(tm.isActive(id)).toBe(true);
    });

    it("isolates interval callback errors", () => {
      const tm = new TimerManager();
      const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
      let count = 0;
      tm.startInterval(() => {
        count++;
        if (count === 2) throw new Error("boom");
      }, 100);

      vi.advanceTimersByTime(500);
      expect(count).toBe(5);
      expect(errorSpy).toHaveBeenCalledTimes(1);
      errorSpy.mockRestore();
    });
  });

  // ===========================================================================
  // stop
  // ===========================================================================
  describe("stop", () => {
    it("cancels a timeout before it fires", () => {
      const tm = new TimerManager();
      const cb = vi.fn();
      const id = tm.start(cb, 1000);
      tm.stop(id);

      vi.advanceTimersByTime(2000);
      expect(cb).not.toHaveBeenCalled();
    });

    it("cancels an interval", () => {
      const tm = new TimerManager();
      const cb = vi.fn();
      const id = tm.startInterval(cb, 100);
      vi.advanceTimersByTime(200);
      expect(cb).toHaveBeenCalledTimes(2);

      tm.stop(id);
      vi.advanceTimersByTime(500);
      expect(cb).toHaveBeenCalledTimes(2);
    });

    it("returns true for active timer", () => {
      const tm = new TimerManager();
      const id = tm.start(() => {}, 1000);
      expect(tm.stop(id)).toBe(true);
    });

    it("returns false for unknown ID", () => {
      const tm = new TimerManager();
      expect(tm.stop(999)).toBe(false);
    });

    it("returns false for already-stopped timer", () => {
      const tm = new TimerManager();
      const id = tm.start(() => {}, 1000);
      tm.stop(id);
      expect(tm.stop(id)).toBe(false);
    });

    it("decrements activeCount", () => {
      const tm = new TimerManager();
      tm.start(() => {}, 1000);
      const id2 = tm.start(() => {}, 2000);
      expect(tm.activeCount).toBe(2);

      tm.stop(id2);
      expect(tm.activeCount).toBe(1);
    });
  });

  // ===========================================================================
  // stopAll
  // ===========================================================================
  describe("stopAll", () => {
    it("cancels all timeouts and intervals", () => {
      const tm = new TimerManager();
      const cb1 = vi.fn();
      const cb2 = vi.fn();
      const cb3 = vi.fn();
      tm.start(cb1, 100);
      tm.start(cb2, 200);
      tm.startInterval(cb3, 50);

      tm.stopAll();
      vi.advanceTimersByTime(1000);

      expect(cb1).not.toHaveBeenCalled();
      expect(cb2).not.toHaveBeenCalled();
      expect(cb3).not.toHaveBeenCalled();
    });

    it("resets activeCount to 0", () => {
      const tm = new TimerManager();
      tm.start(() => {}, 100);
      tm.startInterval(() => {}, 100);
      expect(tm.activeCount).toBe(2);

      tm.stopAll();
      expect(tm.activeCount).toBe(0);
    });

    it("no-op when no timers", () => {
      const tm = new TimerManager();
      expect(() => tm.stopAll()).not.toThrow();
    });
  });

  // ===========================================================================
  // isActive
  // ===========================================================================
  describe("isActive", () => {
    it("returns true for live timeout", () => {
      const tm = new TimerManager();
      const id = tm.start(() => {}, 1000);
      expect(tm.isActive(id)).toBe(true);
    });

    it("returns true for live interval", () => {
      const tm = new TimerManager();
      const id = tm.startInterval(() => {}, 100);
      expect(tm.isActive(id)).toBe(true);
    });

    it("returns false for fired timeout", () => {
      const tm = new TimerManager();
      const id = tm.start(() => {}, 100);
      vi.advanceTimersByTime(100);
      expect(tm.isActive(id)).toBe(false);
    });

    it("returns false for unknown ID", () => {
      const tm = new TimerManager();
      expect(tm.isActive(42)).toBe(false);
    });
  });

  // ===========================================================================
  // getRemainingTime
  // ===========================================================================
  describe("getRemainingTime", () => {
    it("returns remaining ms for a timeout", () => {
      const tm = new TimerManager();
      vi.setSystemTime(new Date(10000));
      const id = tm.start(() => {}, 5000);

      vi.setSystemTime(new Date(12000));
      expect(tm.getRemainingTime(id)).toBe(3000);
    });

    it("returns 0 for intervals", () => {
      const tm = new TimerManager();
      const id = tm.startInterval(() => {}, 100);
      expect(tm.getRemainingTime(id)).toBe(0);
    });

    it("returns 0 for unknown ID", () => {
      const tm = new TimerManager();
      expect(tm.getRemainingTime(999)).toBe(0);
    });

    it("never returns negative (past endTime)", () => {
      const tm = new TimerManager();
      vi.setSystemTime(new Date(10000));
      const id = tm.start(() => {}, 1000);

      // Manually advance system time past endTime without triggering timer
      vi.setSystemTime(new Date(20000));
      expect(tm.getRemainingTime(id)).toBe(0);
    });
  });

  // ===========================================================================
  // getDebugInfo
  // ===========================================================================
  describe("getDebugInfo", () => {
    it("returns empty array when no timers", () => {
      const tm = new TimerManager();
      expect(tm.getDebugInfo()).toEqual([]);
    });

    it("reports timeout with label and remainingMs", () => {
      const tm = new TimerManager();
      vi.setSystemTime(new Date(10000));
      const id = tm.start(() => {}, 5000, "poll");
      vi.setSystemTime(new Date(12000));

      const info = tm.getDebugInfo();
      expect(info).toHaveLength(1);
      expect(info[0]).toEqual({
        id,
        type: "timeout",
        label: "poll",
        remainingMs: 3000,
      });
    });

    it("reports interval with no remainingMs", () => {
      const tm = new TimerManager();
      const id = tm.startInterval(() => {}, 500, "heartbeat");

      const info = tm.getDebugInfo();
      expect(info).toHaveLength(1);
      expect(info[0]).toEqual({
        id,
        type: "interval",
        label: "heartbeat",
        remainingMs: undefined,
      });
    });

    it("reports multiple timers", () => {
      const tm = new TimerManager();
      tm.start(() => {}, 1000);
      tm.startInterval(() => {}, 500);
      expect(tm.getDebugInfo()).toHaveLength(2);
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("stops all timers (alias for stopAll)", () => {
      const tm = new TimerManager();
      const cb = vi.fn();
      tm.start(cb, 100);
      tm.startInterval(cb, 50);

      tm.destroy();
      vi.advanceTimersByTime(1000);

      expect(cb).not.toHaveBeenCalled();
      expect(tm.activeCount).toBe(0);
    });
  });

  // ===========================================================================
  // debug mode
  // ===========================================================================
  describe("debug mode", () => {
    it("logs when debug enabled", () => {
      const debugSpy = vi.spyOn(console, "debug").mockImplementation(() => {});
      const tm = new TimerManager({ debug: true });

      const id = tm.start(() => {}, 1000, "test-timer");
      expect(debugSpy).toHaveBeenCalledWith(expect.stringContaining("Started timeout"));

      tm.stop(id);
      expect(debugSpy).toHaveBeenCalledWith(expect.stringContaining("Stopped timeout"));
      debugSpy.mockRestore();
    });

    it("does not log when debug disabled", () => {
      const debugSpy = vi.spyOn(console, "debug").mockImplementation(() => {});
      const tm = new TimerManager();
      tm.start(() => {}, 1000);
      expect(debugSpy).not.toHaveBeenCalled();
      debugSpy.mockRestore();
    });
  });
});
