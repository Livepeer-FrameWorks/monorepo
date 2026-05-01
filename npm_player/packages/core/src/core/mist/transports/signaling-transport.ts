import type { MistCommand } from "../protocol";
import type {
  MistControlTransport,
  MistOnceHandle,
  MistSendDecorator,
  MistSendListener,
  MistTransportEvents,
  MistTransportState,
} from "../transport";
import { MistWebSocketTransport, type MistWebSocketTransportOptions } from "./websocket-transport";

export class MistSignalingTransport implements MistControlTransport<MistCommand> {
  private readonly base: MistWebSocketTransport;

  constructor(url: string, options: MistWebSocketTransportOptions = {}) {
    this.base = new MistWebSocketTransport(url, options);
  }

  get state(): MistTransportState {
    return this.base.state;
  }

  get capabilities() {
    return this.base.capabilities;
  }

  on<K extends keyof MistTransportEvents>(
    event: K,
    listener: (data: MistTransportEvents[K]) => void
  ): () => void {
    return this.base.on(event, listener);
  }

  off<K extends keyof MistTransportEvents>(
    event: K,
    listener: (data: MistTransportEvents[K]) => void
  ): void {
    this.base.off(event, listener);
  }

  connect(): Promise<void> {
    return this.base.connect();
  }

  disconnect(reason?: string): void {
    void reason;
    this.base.disconnect();
  }

  destroy(): void {
    this.base.destroy();
  }

  send(cmd: MistCommand): boolean {
    return this.base.send(cmd);
  }

  addSendDecorator(d: MistSendDecorator<MistCommand>): () => void {
    return this.base.addSendDecorator(d);
  }

  addSendListener(l: MistSendListener<MistCommand>): () => void {
    return this.base.addSendListener(l);
  }

  once<T extends import("../protocol").MistEvent["type"]>(
    type: T
  ): MistOnceHandle<Extract<import("../protocol").MistEvent, { type: T }>> {
    return this.base.once(type);
  }
}
