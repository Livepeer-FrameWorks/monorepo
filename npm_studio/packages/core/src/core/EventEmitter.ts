/**
 * Typed Event Emitter
 * A type-safe event emitter for StreamCrafter
 */

type EventHandler<T> = (data: T) => void;

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type EventMap = { [key: string]: any };

export class TypedEventEmitter<Events extends EventMap> {
  private listeners = new Map<keyof Events, Set<EventHandler<unknown>>>();

  /**
   * Subscribe to an event
   */
  on<K extends keyof Events>(event: K, handler: EventHandler<Events[K]>): () => void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(handler as EventHandler<unknown>);

    // Return unsubscribe function
    return () => this.off(event, handler);
  }

  /**
   * Subscribe to an event (once)
   */
  once<K extends keyof Events>(event: K, handler: EventHandler<Events[K]>): () => void {
    const wrappedHandler: EventHandler<Events[K]> = (data) => {
      this.off(event, wrappedHandler);
      handler(data);
    };
    return this.on(event, wrappedHandler);
  }

  /**
   * Unsubscribe from an event
   */
  off<K extends keyof Events>(event: K, handler: EventHandler<Events[K]>): void {
    const handlers = this.listeners.get(event);
    if (handlers) {
      handlers.delete(handler as EventHandler<unknown>);
      if (handlers.size === 0) {
        this.listeners.delete(event);
      }
    }
  }

  /**
   * Emit an event
   */
  protected emit<K extends keyof Events>(event: K, data: Events[K]): void {
    const handlers = this.listeners.get(event);
    if (handlers) {
      handlers.forEach((handler) => {
        try {
          handler(data);
        } catch (error) {
          console.error(`Error in event handler for "${String(event)}":`, error);
        }
      });
    }
  }

  /**
   * Remove all listeners for an event (or all events if no event specified)
   */
  removeAllListeners<K extends keyof Events>(event?: K): void {
    if (event) {
      this.listeners.delete(event);
    } else {
      this.listeners.clear();
    }
  }

  /**
   * Get the number of listeners for an event
   */
  listenerCount<K extends keyof Events>(event: K): number {
    return this.listeners.get(event)?.size ?? 0;
  }
}
