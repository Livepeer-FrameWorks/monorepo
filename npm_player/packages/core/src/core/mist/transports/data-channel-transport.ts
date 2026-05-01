import type {
  MistControlTransport,
  MistOnceHandle,
  MistSendDecorator,
  MistSendListener,
  MistTransportEvents,
  MistTransportState,
} from "../transport";
import type { MistCommand, MistEvent } from "../protocol";

export class MistDataChannelTransport implements MistControlTransport<MistCommand> {
  public state: MistTransportState = "disconnected";
  public readonly capabilities = { binary: false, reconnect: false };

  private listeners = new Map<keyof MistTransportEvents, Set<Function>>();
  private sendDecorators = new Set<MistSendDecorator<MistCommand>>();
  private sendListeners = new Set<MistSendListener<MistCommand>>();

  constructor(private readonly channel: RTCDataChannel) {
    channel.addEventListener("open", () => this.setState("connected"));
    channel.addEventListener("close", () => this.setState("disconnected"));
    channel.addEventListener("message", (event) => {
      if (typeof event.data !== "string") {
        return;
      }
      try {
        const raw = JSON.parse(event.data);
        const payload = "data" in raw ? raw.data : raw;
        this.emit("event", { event: { type: raw.type, ...payload } });
      } catch (cause) {
        this.emit("error", { message: "Failed to parse data channel message", cause });
      }
    });

    if (channel.readyState === "open") {
      this.state = "connected";
    }
  }

  on<K extends keyof MistTransportEvents>(
    event: K,
    listener: (data: MistTransportEvents[K]) => void
  ): () => void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);
    return () => this.off(event, listener);
  }

  off<K extends keyof MistTransportEvents>(
    event: K,
    listener: (data: MistTransportEvents[K]) => void
  ): void {
    this.listeners.get(event)?.delete(listener);
  }

  connect(): Promise<void> {
    if (this.channel.readyState === "open") {
      this.setState("connected");
      return Promise.resolve();
    }
    this.setState("connecting");
    return Promise.resolve();
  }

  disconnect(): void {
    this.channel.close();
    this.setState("disconnected");
  }

  destroy(): void {
    this.disconnect();
    this.setState("closed");
    this.listeners.clear();
  }

  send(cmd: MistCommand): boolean {
    if (this.channel.readyState !== "open") {
      return false;
    }

    let next: MistCommand | null = cmd;
    for (const decorator of this.sendDecorators) {
      next = next ? decorator(next) : null;
      if (!next) return false;
    }

    for (const listener of this.sendListeners) {
      listener(next);
    }

    this.channel.send(JSON.stringify(next));
    return true;
  }

  addSendDecorator(d: MistSendDecorator<MistCommand>): () => void {
    this.sendDecorators.add(d);
    return () => this.sendDecorators.delete(d);
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

  private setState(state: MistTransportState): void {
    this.state = state;
    this.emit("statechange", { state });
  }

  private emit<K extends keyof MistTransportEvents>(event: K, data: MistTransportEvents[K]): void {
    this.listeners.get(event)?.forEach((listener) => listener(data));
  }
}
