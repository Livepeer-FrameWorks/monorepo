import type { MistCommand, MistEvent } from "../../src/core/mist/protocol";
import type {
  MistMediaTransport,
  MistOnceHandle,
  MistSendDecorator,
  MistSendListener,
  MistTransportEvents,
  MistTransportState,
} from "../../src/core/mist/transport";

export class FakeTransport implements MistMediaTransport {
  state: MistTransportState = "connected";
  readonly capabilities = { binary: true, reconnect: false };
  readonly sent: MistCommand[] = [];
  private readonly listeners = new Map<keyof MistTransportEvents, Set<Function>>();
  private readonly decorators = new Set<MistSendDecorator<MistCommand>>();
  private readonly sendListeners = new Set<MistSendListener<MistCommand>>();

  on<K extends keyof MistTransportEvents>(
    event: K,
    listener: (data: MistTransportEvents[K]) => void
  ): () => void {
    if (!this.listeners.has(event)) this.listeners.set(event, new Set());
    this.listeners.get(event)!.add(listener);
    return () => this.off(event, listener);
  }

  off<K extends keyof MistTransportEvents>(
    event: K,
    listener: (data: MistTransportEvents[K]) => void
  ): void {
    this.listeners.get(event)?.delete(listener);
  }

  send(cmd: MistCommand): boolean {
    let next: MistCommand | null = cmd;
    for (const decorator of this.decorators) {
      next = next ? decorator(next) : null;
      if (!next) return false;
    }
    for (const listener of this.sendListeners) listener(next);
    this.sent.push(next);
    return true;
  }

  addSendDecorator(d: MistSendDecorator<MistCommand>): () => void {
    this.decorators.add(d);
    return () => this.decorators.delete(d);
  }

  addSendListener(l: MistSendListener<MistCommand>): () => void {
    this.sendListeners.add(l);
    return () => this.sendListeners.delete(l);
  }

  once<T extends MistEvent["type"]>(type: T): MistOnceHandle<Extract<MistEvent, { type: T }>> {
    let settled = false;
    let offEvent: (() => void) | null = null;
    let offState: (() => void) | null = null;
    let rejectPromise: ((reason?: unknown) => void) | null = null;
    const cleanup = () => {
      offEvent?.();
      offState?.();
      offEvent = null;
      offState = null;
    };
    const promise = new Promise<Extract<MistEvent, { type: T }>>((resolve, reject) => {
      rejectPromise = reject;
      offEvent = this.on("event", ({ event }) => {
        if (event.type === type) {
          if (settled) return;
          settled = true;
          cleanup();
          resolve(event as Extract<MistEvent, { type: T }>);
        }
      });
      offState = this.on("statechange", ({ state }) => {
        if (state === "disconnected" || state === "closed") {
          if (settled) return;
          settled = true;
          cleanup();
          reject(new Error("transport closed"));
        }
      });
    });
    return {
      promise,
      cancel: (reason = "cancelled") => {
        if (settled) return;
        settled = true;
        cleanup();
        rejectPromise?.(new Error(reason));
      },
    };
  }

  async connect(): Promise<void> {}

  disconnect(): void {
    this.state = "disconnected";
    this.emit("statechange", { state: "disconnected" });
  }

  destroy(): void {
    this.state = "closed";
    this.listeners.clear();
  }

  emitEvent(event: MistEvent): void {
    this.emit("event", { event });
  }

  private emit<K extends keyof MistTransportEvents>(event: K, data: MistTransportEvents[K]): void {
    this.listeners.get(event)?.forEach((listener) => listener(data));
  }
}
