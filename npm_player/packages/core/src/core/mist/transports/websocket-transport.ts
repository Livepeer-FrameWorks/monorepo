import type {
  MistControlTransport,
  MistOnceHandle,
  MistSendDecorator,
  MistSendListener,
  MistTransportEvents,
  MistTransportState,
} from "../transport";
import type { MistCommand, MistEvent } from "../protocol";

export interface MistWebSocketTransportOptions {
  maxReconnectAttempts?: number;
  reconnectDelayMs?: number;
  maxReconnectDelayMs?: number;
}

const DEFAULTS: Required<MistWebSocketTransportOptions> = {
  maxReconnectAttempts: 5,
  reconnectDelayMs: 1000,
  maxReconnectDelayMs: 30000,
};

export class MistWebSocketTransport implements MistControlTransport<MistCommand> {
  public state: MistTransportState = "disconnected";
  public readonly capabilities = { binary: true, reconnect: true };

  private ws: WebSocket | null = null;
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly decorators = new Set<MistSendDecorator<MistCommand>>();
  private readonly sendListeners = new Set<MistSendListener<MistCommand>>();
  private closed = false;
  private listeners = new Map<keyof MistTransportEvents, Set<Function>>();

  constructor(
    private readonly url: string,
    private readonly options: MistWebSocketTransportOptions = {}
  ) {}

  async connect(): Promise<void> {
    this.closed = false;
    await this.openSocket();
  }

  disconnect(): void {
    this.closed = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.ws?.close();
    this.ws = null;
    this.setState("disconnected");
  }

  destroy(): void {
    this.disconnect();
    this.setState("closed");
    this.listeners.clear();
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

  send(cmd: MistCommand): boolean {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return false;
    }

    let next: MistCommand | null = cmd;
    for (const decorate of this.decorators) {
      next = next ? decorate(next) : null;
      if (!next) return false;
    }

    for (const listener of this.sendListeners) {
      listener(next);
    }

    try {
      this.ws.send(JSON.stringify(next));
      return true;
    } catch (cause) {
      this.emit("error", { message: "WebSocket send failed", cause });
      return false;
    }
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

  private async openSocket(): Promise<void> {
    this.setState(this.reconnectAttempts > 0 ? "reconnecting" : "connecting");

    await new Promise<void>((resolve, reject) => {
      let ws: WebSocket;
      try {
        ws = new WebSocket(this.url);
      } catch (cause) {
        this.setState("disconnected");
        reject(cause);
        return;
      }

      ws.binaryType = "arraybuffer";
      this.ws = ws;

      ws.onopen = () => {
        this.reconnectAttempts = 0;
        this.setState("connected");
        resolve();
      };

      ws.onmessage = (message) => this.handleMessage(message.data);
      ws.onerror = () => this.emit("error", { message: "WebSocket error" });

      ws.onclose = (event) => {
        if (this.closed) {
          this.setState("disconnected", event?.code);
          return;
        }

        if (this.shouldReconnect()) {
          this.scheduleReconnect();
        } else {
          this.setState("disconnected", event?.code);
        }
        reject(new Error("closed"));
      };
    });
  }

  private handleMessage(raw: string | ArrayBuffer): void {
    if (raw instanceof ArrayBuffer) {
      this.emit("binary", { data: raw });
      return;
    }

    try {
      const data = JSON.parse(raw);
      if ("time" in data && "track" in data && "data" in data) {
        this.emit("event", { event: { type: "metadata", ...data } });
        return;
      }

      const payload = "data" in data ? data.data : data;
      this.emit("event", { event: { type: data.type, ...payload } });
    } catch (cause) {
      this.emit("error", { message: "Failed to parse message", cause });
    }
  }

  private shouldReconnect(): boolean {
    const { maxReconnectAttempts } = { ...DEFAULTS, ...this.options };
    return maxReconnectAttempts === 0 || this.reconnectAttempts < maxReconnectAttempts;
  }

  private scheduleReconnect(): void {
    const { reconnectDelayMs, maxReconnectDelayMs } = { ...DEFAULTS, ...this.options };
    this.reconnectAttempts += 1;
    const delay = Math.min(
      reconnectDelayMs * 2 ** (this.reconnectAttempts - 1),
      maxReconnectDelayMs
    );
    this.reconnectTimer = setTimeout(() => {
      void this.openSocket().catch(() => {});
    }, delay);
  }

  private setState(state: MistTransportState, code?: number): void {
    this.state = state;
    this.emit("statechange", { state, code });
  }

  private emit<K extends keyof MistTransportEvents>(event: K, data: MistTransportEvents[K]): void {
    this.listeners.get(event)?.forEach((listener) => listener(data));
  }
}
