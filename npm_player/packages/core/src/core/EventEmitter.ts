/**
 * EventEmitter.ts
 *
 * A lightweight, typed event emitter for framework-agnostic components.
 * Used by GatewayClient, StreamStateClient, and PlayerController.
 */

type Listener<T> = (data: T) => void;

/**
 * Typed event emitter that provides type-safe event handling.
 *
 * @example
 * ```typescript
 * interface MyEvents {
 *   stateChange: { state: string };
 *   error: { message: string };
 * }
 *
 * class MyClass extends TypedEventEmitter<MyEvents> {
 *   doSomething() {
 *     this.emit('stateChange', { state: 'ready' });
 *   }
 * }
 *
 * const instance = new MyClass();
 * const unsub = instance.on('stateChange', ({ state }) => console.log(state));
 * unsub(); // unsubscribe
 * ```
 */
export class TypedEventEmitter<Events extends Record<string, any>> {
  private listeners = new Map<keyof Events, Set<Function>>();

  /**
   * Subscribe to an event.
   * @param event - The event name to listen for
   * @param listener - Callback function invoked when the event is emitted
   * @returns Unsubscribe function
   */
  on<K extends keyof Events>(event: K, listener: Listener<Events[K]>): () => void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);

    // Return unsubscribe function
    return () => this.off(event, listener);
  }

  /**
   * Subscribe to an event only once.
   * The listener is automatically removed after the first invocation.
   * @param event - The event name to listen for
   * @param listener - Callback function invoked when the event is emitted
   * @returns Unsubscribe function
   */
  once<K extends keyof Events>(event: K, listener: Listener<Events[K]>): () => void {
    const onceListener = (data: Events[K]) => {
      this.off(event, onceListener);
      listener(data);
    };
    return this.on(event, onceListener);
  }

  /**
   * Unsubscribe from an event.
   * @param event - The event name
   * @param listener - The callback to remove
   */
  off<K extends keyof Events>(event: K, listener: Listener<Events[K]>): void {
    this.listeners.get(event)?.delete(listener);
  }

  /**
   * Emit an event to all subscribers.
   * @param event - The event name
   * @param data - The event payload
   */
  protected emit<K extends keyof Events>(event: K, data: Events[K]): void {
    this.listeners.get(event)?.forEach(listener => {
      try {
        listener(data);
      } catch (e) {
        console.error(`[EventEmitter] Error in ${String(event)} listener:`, e);
      }
    });
  }

  /**
   * Remove all listeners for all events.
   */
  removeAllListeners(): void {
    this.listeners.clear();
  }

  /**
   * Remove all listeners for a specific event.
   * @param event - The event name
   */
  removeListeners<K extends keyof Events>(event: K): void {
    this.listeners.delete(event);
  }

  /**
   * Check if there are any listeners for an event.
   * @param event - The event name
   */
  hasListeners<K extends keyof Events>(event: K): boolean {
    return (this.listeners.get(event)?.size ?? 0) > 0;
  }
}

export default TypedEventEmitter;
