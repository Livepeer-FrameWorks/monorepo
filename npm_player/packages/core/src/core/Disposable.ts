/**
 * Disposable interface for consistent cleanup across all core classes.
 *
 * All classes that manage resources (timers, event listeners, WebSockets, etc.)
 * should implement this interface to ensure proper cleanup.
 */

/**
 * Interface for objects that need cleanup
 */
export interface Disposable {
  /**
   * Clean up all resources held by this object.
   * Safe to call multiple times - subsequent calls should be no-ops.
   */
  dispose(): void;

  /**
   * Whether this object has been disposed
   */
  readonly disposed: boolean;
}

/**
 * Base class for disposable objects that provides:
 * - disposed flag tracking
 * - Double-dispose protection
 * - Template method for subclass cleanup
 */
export abstract class BaseDisposable implements Disposable {
  private _disposed = false;

  /**
   * Whether this object has been disposed
   */
  get disposed(): boolean {
    return this._disposed;
  }

  /**
   * Dispose of this object, releasing all resources.
   * Safe to call multiple times.
   */
  dispose(): void {
    if (this._disposed) return;
    this._disposed = true;
    this.onDispose();
  }

  /**
   * Subclasses implement this to clean up their resources.
   * Called exactly once when dispose() is first called.
   */
  protected abstract onDispose(): void;

  /**
   * Throw if this object has been disposed.
   * Use at the start of methods that shouldn't run after disposal.
   */
  protected throwIfDisposed(operation: string = 'operation'): void {
    if (this._disposed) {
      throw new Error(`Cannot perform ${operation} on disposed object`);
    }
  }

  /**
   * Check if disposed without throwing - useful for conditional guards
   */
  protected guardDisposed(): boolean {
    return this._disposed;
  }
}

/**
 * Utility to dispose multiple disposables at once
 */
export function disposeAll(...disposables: (Disposable | null | undefined)[]): void {
  for (const d of disposables) {
    if (d && !d.disposed) {
      try {
        d.dispose();
      } catch (err) {
        console.warn('[Disposable] Error during disposal:', err);
      }
    }
  }
}

/**
 * Create a composite disposable that disposes multiple items
 */
export function createCompositeDisposable(
  ...disposables: (Disposable | (() => void))[]
): Disposable {
  let disposed = false;

  return {
    get disposed() {
      return disposed;
    },
    dispose() {
      if (disposed) return;
      disposed = true;

      for (const d of disposables) {
        try {
          if (typeof d === 'function') {
            d();
          } else if (d && !d.disposed) {
            d.dispose();
          }
        } catch (err) {
          console.warn('[CompositeDisposable] Error during disposal:', err);
        }
      }
    },
  };
}

export default BaseDisposable;
