import { describe, expect, it, vi } from "vitest";

import {
  BaseDisposable,
  disposeAll,
  createCompositeDisposable,
  type Disposable,
} from "../src/core/Disposable";

// Concrete subclass for testing abstract BaseDisposable
class TestDisposable extends BaseDisposable {
  public onDisposeCalled = false;
  public onDisposeCallback?: () => void;

  protected onDispose(): void {
    this.onDisposeCalled = true;
    this.onDisposeCallback?.();
  }

  // Expose protected methods for testing
  public testThrowIfDisposed(op?: string): void {
    this.throwIfDisposed(op);
  }

  public testGuardDisposed(): boolean {
    return this.guardDisposed();
  }
}

describe("Disposable", () => {
  // =========================================================================
  // BaseDisposable
  // =========================================================================
  describe("BaseDisposable", () => {
    it("starts not disposed", () => {
      const d = new TestDisposable();
      expect(d.disposed).toBe(false);
    });

    it("dispose sets disposed flag", () => {
      const d = new TestDisposable();
      d.dispose();
      expect(d.disposed).toBe(true);
    });

    it("dispose calls onDispose exactly once", () => {
      const d = new TestDisposable();
      d.dispose();
      expect(d.onDisposeCalled).toBe(true);
    });

    it("double dispose is a no-op", () => {
      const callback = vi.fn();
      const d = new TestDisposable();
      d.onDisposeCallback = callback;

      d.dispose();
      d.dispose();

      expect(callback).toHaveBeenCalledTimes(1);
      expect(d.disposed).toBe(true);
    });

    it("throwIfDisposed does not throw before disposal", () => {
      const d = new TestDisposable();
      expect(() => d.testThrowIfDisposed()).not.toThrow();
    });

    it("throwIfDisposed throws after disposal", () => {
      const d = new TestDisposable();
      d.dispose();
      expect(() => d.testThrowIfDisposed()).toThrow("Cannot perform operation on disposed object");
    });

    it("throwIfDisposed includes custom operation name", () => {
      const d = new TestDisposable();
      d.dispose();
      expect(() => d.testThrowIfDisposed("play")).toThrow("Cannot perform play on disposed object");
    });

    it("guardDisposed returns false before disposal", () => {
      const d = new TestDisposable();
      expect(d.testGuardDisposed()).toBe(false);
    });

    it("guardDisposed returns true after disposal", () => {
      const d = new TestDisposable();
      d.dispose();
      expect(d.testGuardDisposed()).toBe(true);
    });
  });

  // =========================================================================
  // disposeAll
  // =========================================================================
  describe("disposeAll", () => {
    it("disposes all items", () => {
      const d1 = new TestDisposable();
      const d2 = new TestDisposable();
      disposeAll(d1, d2);
      expect(d1.disposed).toBe(true);
      expect(d2.disposed).toBe(true);
    });

    it("skips null and undefined", () => {
      const d1 = new TestDisposable();
      disposeAll(null, d1, undefined);
      expect(d1.disposed).toBe(true);
    });

    it("skips already disposed items", () => {
      const callback = vi.fn();
      const d1 = new TestDisposable();
      d1.onDisposeCallback = callback;
      d1.dispose(); // pre-dispose
      callback.mockClear();

      disposeAll(d1);
      expect(callback).not.toHaveBeenCalled(); // not called again
    });

    it("continues after error in one disposal", () => {
      const bad = new TestDisposable();
      bad.onDisposeCallback = () => {
        throw new Error("boom");
      };
      const good = new TestDisposable();

      disposeAll(bad, good);
      expect(bad.disposed).toBe(true);
      expect(good.disposed).toBe(true);
    });

    it("handles empty args", () => {
      expect(() => disposeAll()).not.toThrow();
    });
  });

  // =========================================================================
  // createCompositeDisposable
  // =========================================================================
  describe("createCompositeDisposable", () => {
    it("disposes all contained Disposables", () => {
      const d1 = new TestDisposable();
      const d2 = new TestDisposable();
      const composite = createCompositeDisposable(d1, d2);

      expect(composite.disposed).toBe(false);
      composite.dispose();
      expect(composite.disposed).toBe(true);
      expect(d1.disposed).toBe(true);
      expect(d2.disposed).toBe(true);
    });

    it("calls cleanup functions", () => {
      const fn1 = vi.fn();
      const fn2 = vi.fn();
      const composite = createCompositeDisposable(fn1, fn2);

      composite.dispose();
      expect(fn1).toHaveBeenCalledTimes(1);
      expect(fn2).toHaveBeenCalledTimes(1);
    });

    it("mixes Disposables and functions", () => {
      const d1 = new TestDisposable();
      const fn = vi.fn();
      const composite = createCompositeDisposable(d1, fn);

      composite.dispose();
      expect(d1.disposed).toBe(true);
      expect(fn).toHaveBeenCalledTimes(1);
    });

    it("double dispose is a no-op", () => {
      const fn = vi.fn();
      const composite = createCompositeDisposable(fn);

      composite.dispose();
      composite.dispose();
      expect(fn).toHaveBeenCalledTimes(1);
    });

    it("skips already-disposed items", () => {
      const d1 = new TestDisposable();
      d1.dispose(); // pre-dispose
      const callback = vi.fn();
      d1.onDisposeCallback = callback;

      const composite = createCompositeDisposable(d1);
      composite.dispose();

      // d1 was already disposed, so its callback shouldn't fire again
      expect(callback).not.toHaveBeenCalled();
    });

    it("continues after error in one item", () => {
      const badFn = vi.fn(() => {
        throw new Error("boom");
      });
      const goodFn = vi.fn();
      const composite = createCompositeDisposable(badFn, goodFn);

      composite.dispose();
      expect(badFn).toHaveBeenCalledTimes(1);
      expect(goodFn).toHaveBeenCalledTimes(1);
    });

    it("empty composite works fine", () => {
      const composite = createCompositeDisposable();
      expect(composite.disposed).toBe(false);
      composite.dispose();
      expect(composite.disposed).toBe(true);
    });
  });
});
