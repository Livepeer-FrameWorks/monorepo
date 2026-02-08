import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  TransitionEngine,
  createDefaultTransitionConfig,
  createCutTransition,
  createFadeTransition,
  createSlideTransition,
  getAvailableTransitionTypes,
  getAvailableEasingTypes,
  validateTransitionConfig,
} from "../src/core/TransitionEngine";

describe("TransitionEngine", () => {
  let engine: TransitionEngine;
  let now: number;

  beforeEach(() => {
    engine = new TransitionEngine();
    now = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => now);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // =========================================================================
  // Initial state
  // =========================================================================
  describe("initial state", () => {
    it("isActive returns false", () => {
      expect(engine.isActive()).toBe(false);
    });

    it("getProgress returns 0", () => {
      expect(engine.getProgress()).toBe(0);
    });

    it("getRawProgress returns 0", () => {
      expect(engine.getRawProgress()).toBe(0);
    });

    it("getType returns cut", () => {
      expect(engine.getType()).toBe("cut");
    });

    it("getFromSceneId returns null", () => {
      expect(engine.getFromSceneId()).toBeNull();
    });

    it("getToSceneId returns null", () => {
      expect(engine.getToSceneId()).toBeNull();
    });

    it("getRemainingTime returns 0", () => {
      expect(engine.getRemainingTime()).toBe(0);
    });

    it("getState returns null", () => {
      expect(engine.getState()).toBeNull();
    });

    it("update returns null", () => {
      expect(engine.update()).toBeNull();
    });
  });

  // =========================================================================
  // start() + update() — fade transition
  // =========================================================================
  describe("fade transition", () => {
    it("starts active with progress 0", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      expect(engine.isActive()).toBe(true);
      expect(engine.getProgress()).toBe(0);
      expect(engine.getFromSceneId()).toBe("A");
      expect(engine.getToSceneId()).toBe("B");
    });

    it("update at midpoint returns ~0.5 progress (linear)", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "linear" });
      now = 1500; // 500ms elapsed
      const state = engine.update();
      expect(state).not.toBeNull();
      expect(state!.progress).toBeCloseTo(0.5, 5);
      expect(state!.active).toBe(true);
    });

    it("update at full duration completes", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      now = 1500; // 500ms elapsed
      const state = engine.update();
      expect(state!.progress).toBe(1);
      expect(state!.active).toBe(false);
      expect(engine.isActive()).toBe(false);
    });

    it("update beyond duration clamps to 1", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      now = 5000;
      const state = engine.update();
      expect(state!.progress).toBe(1);
      expect(state!.active).toBe(false);
    });

    it("returns null after transition completes", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      now = 2000;
      engine.update(); // completes
      expect(engine.update()).toBeNull();
    });
  });

  // =========================================================================
  // Easing functions
  // =========================================================================
  describe("easing", () => {
    it("ease-in: progress < raw at midpoint", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "ease-in" });
      now = 1500;
      engine.update();
      // ease-in: t^2, at t=0.5 → 0.25
      expect(engine.getProgress()).toBeCloseTo(0.25, 5);
      expect(engine.getProgress()).toBeLessThan(engine.getRawProgress());
    });

    it("ease-out: progress > raw at midpoint", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "ease-out" });
      now = 1500;
      engine.update();
      // ease-out: t*(2-t), at t=0.5 → 0.75
      expect(engine.getProgress()).toBeCloseTo(0.75, 5);
      expect(engine.getProgress()).toBeGreaterThan(engine.getRawProgress());
    });

    it("ease-in-out: progress ≈ 0.5 at midpoint", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "ease-in-out" });
      now = 1500;
      engine.update();
      // ease-in-out at t=0.5: 2*(0.5)^2 = 0.5
      expect(engine.getProgress()).toBeCloseTo(0.5, 5);
    });

    it("linear: progress = raw progress", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "linear" });
      now = 1300;
      engine.update();
      expect(engine.getProgress()).toBeCloseTo(0.3, 5);
    });

    it("all easings reach 1 at completion", () => {
      for (const easing of ["linear", "ease-in", "ease-out", "ease-in-out"] as const) {
        const e = new TransitionEngine();
        vi.spyOn(performance, "now").mockReturnValue(1000);
        e.start("A", "B", { type: "fade", durationMs: 500, easing });
        vi.spyOn(performance, "now").mockReturnValue(1500);
        e.update();
        expect(e.getProgress()).toBe(1);
      }
    });

    it("unknown easing falls back to linear", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "bogus" as any });
      now = 1500;
      engine.update();
      expect(engine.getProgress()).toBeCloseTo(0.5, 5);
    });
  });

  // =========================================================================
  // Cut transition — instant
  // =========================================================================
  describe("cut transition", () => {
    it("immediately completes with progress 1", () => {
      engine.start("A", "B", { type: "cut", durationMs: 0, easing: "linear" });
      expect(engine.isActive()).toBe(false);
      expect(engine.getProgress()).toBe(1);
    });

    it("scene IDs are set even after instant complete", () => {
      engine.start("A", "B", { type: "cut", durationMs: 0, easing: "linear" });
      expect(engine.getFromSceneId()).toBe("A");
      expect(engine.getToSceneId()).toBe("B");
    });
  });

  // =========================================================================
  // getRawProgress
  // =========================================================================
  describe("getRawProgress", () => {
    it("returns linear progress regardless of easing", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "ease-in" });
      now = 1500;
      engine.update();
      expect(engine.getRawProgress()).toBeCloseTo(0.5, 5);
    });

    it("returns last progress when inactive", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      now = 1500;
      engine.update();
      expect(engine.getRawProgress()).toBe(1);
    });
  });

  // =========================================================================
  // getRemainingTime
  // =========================================================================
  describe("getRemainingTime", () => {
    it("returns 0 when no transition", () => {
      expect(engine.getRemainingTime()).toBe(0);
    });

    it("returns correct remaining time mid-transition", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "linear" });
      now = 1300;
      expect(engine.getRemainingTime()).toBeCloseTo(700, 0);
    });

    it("returns 0 after completion", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      now = 2000;
      engine.update();
      expect(engine.getRemainingTime()).toBe(0);
    });

    it("clamps to 0 when elapsed > duration", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      now = 5000;
      // Still active (update not called yet), but elapsed > duration
      expect(engine.getRemainingTime()).toBe(0);
    });
  });

  // =========================================================================
  // getState — snapshot
  // =========================================================================
  describe("getState", () => {
    it("returns a copy, not the internal state", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      const s1 = engine.getState();
      const s2 = engine.getState();
      expect(s1).toEqual(s2);
      expect(s1).not.toBe(s2);
    });

    it("contains all expected fields", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "linear" });
      const s = engine.getState()!;
      expect(s.active).toBe(true);
      expect(s.type).toBe("fade");
      expect(s.progress).toBe(0);
      expect(s.fromSceneId).toBe("A");
      expect(s.toSceneId).toBe("B");
      expect(s.startTime).toBe(1000);
      expect(s.durationMs).toBe(500);
    });
  });

  // =========================================================================
  // cancel / complete / reset
  // =========================================================================
  describe("cancel", () => {
    it("stops transition at current progress", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "linear" });
      now = 1300;
      engine.update();
      engine.cancel();
      expect(engine.isActive()).toBe(false);
      expect(engine.getProgress()).toBeCloseTo(0.3, 2);
    });

    it("no-ops when no transition", () => {
      engine.cancel(); // should not throw
      expect(engine.isActive()).toBe(false);
    });
  });

  describe("complete", () => {
    it("jumps to progress 1", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "linear" });
      engine.complete();
      expect(engine.isActive()).toBe(false);
      expect(engine.getProgress()).toBe(1);
    });

    it("no-ops when no transition", () => {
      engine.complete();
      expect(engine.getProgress()).toBe(0);
    });
  });

  describe("reset", () => {
    it("clears all state", () => {
      engine.start("A", "B", { type: "fade", durationMs: 500, easing: "ease-in" });
      engine.reset();
      expect(engine.isActive()).toBe(false);
      expect(engine.getState()).toBeNull();
      expect(engine.getFromSceneId()).toBeNull();
      expect(engine.getToSceneId()).toBeNull();
      expect(engine.getProgress()).toBe(0);
    });
  });

  // =========================================================================
  // Starting a new transition replaces previous
  // =========================================================================
  describe("transition replacement", () => {
    it("start replaces a running transition", () => {
      engine.start("A", "B", { type: "fade", durationMs: 1000, easing: "linear" });
      engine.start("B", "C", { type: "fade", durationMs: 2000, easing: "ease-in" });
      expect(engine.getFromSceneId()).toBe("B");
      expect(engine.getToSceneId()).toBe("C");
      expect(engine.isActive()).toBe(true);
      expect(engine.getProgress()).toBe(0);
    });
  });

  // =========================================================================
  // Factory functions
  // =========================================================================
  describe("createDefaultTransitionConfig", () => {
    it("returns fade / 500ms / ease-in-out", () => {
      const c = createDefaultTransitionConfig();
      expect(c.type).toBe("fade");
      expect(c.durationMs).toBe(500);
      expect(c.easing).toBe("ease-in-out");
    });
  });

  describe("createCutTransition", () => {
    it("returns cut / 0ms / linear", () => {
      const c = createCutTransition();
      expect(c.type).toBe("cut");
      expect(c.durationMs).toBe(0);
      expect(c.easing).toBe("linear");
    });
  });

  describe("createFadeTransition", () => {
    it("defaults to 500ms / ease-in-out", () => {
      const c = createFadeTransition();
      expect(c.type).toBe("fade");
      expect(c.durationMs).toBe(500);
      expect(c.easing).toBe("ease-in-out");
    });

    it("accepts custom duration and easing", () => {
      const c = createFadeTransition(1000, "ease-in");
      expect(c.durationMs).toBe(1000);
      expect(c.easing).toBe("ease-in");
    });
  });

  describe("createSlideTransition", () => {
    it("defaults to slide-left / 500ms / ease-in-out", () => {
      const c = createSlideTransition();
      expect(c.type).toBe("slide-left");
      expect(c.durationMs).toBe(500);
      expect(c.easing).toBe("ease-in-out");
    });

    it.each(["left", "right", "up", "down"] as const)("creates slide-%s", (dir) => {
      const c = createSlideTransition(dir);
      expect(c.type).toBe(`slide-${dir}`);
    });

    it("accepts custom duration and easing", () => {
      const c = createSlideTransition("right", 800, "linear");
      expect(c.durationMs).toBe(800);
      expect(c.easing).toBe("linear");
    });
  });

  // =========================================================================
  // Utility functions
  // =========================================================================
  describe("getAvailableTransitionTypes", () => {
    it("returns all 6 types", () => {
      const types = getAvailableTransitionTypes();
      expect(types).toHaveLength(6);
      expect(types).toContain("cut");
      expect(types).toContain("fade");
      expect(types).toContain("slide-left");
      expect(types).toContain("slide-right");
      expect(types).toContain("slide-up");
      expect(types).toContain("slide-down");
    });
  });

  describe("getAvailableEasingTypes", () => {
    it("returns all 4 easing types", () => {
      const types = getAvailableEasingTypes();
      expect(types).toHaveLength(4);
      expect(types).toContain("linear");
      expect(types).toContain("ease-in");
      expect(types).toContain("ease-out");
      expect(types).toContain("ease-in-out");
    });
  });

  describe("validateTransitionConfig", () => {
    it("fills defaults for empty config", () => {
      const c = validateTransitionConfig({});
      expect(c.type).toBe("fade");
      expect(c.durationMs).toBe(500);
      expect(c.easing).toBe("ease-in-out");
    });

    it("preserves provided values", () => {
      const c = validateTransitionConfig({ type: "cut", durationMs: 100, easing: "linear" });
      expect(c.type).toBe("cut");
      expect(c.durationMs).toBe(100);
      expect(c.easing).toBe("linear");
    });

    it("clamps negative duration to 0", () => {
      const c = validateTransitionConfig({ durationMs: -100 });
      expect(c.durationMs).toBe(0);
    });

    it("preserves 0 duration", () => {
      const c = validateTransitionConfig({ durationMs: 0 });
      expect(c.durationMs).toBe(0);
    });
  });
});
