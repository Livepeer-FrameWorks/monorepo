import type {
  MistControlTransport,
  MistMetadataTransport,
  MistOnceHandle,
  MistSendDecorator,
  MistSendListener,
  MistTransportEvents,
  MistTransportState,
} from "../transport";
import type { MistEvent, MistMetadataCommand } from "../protocol";
import { MistWebSocketTransport, type MistWebSocketTransportOptions } from "./websocket-transport";

export class MistMetadataWsTransport
  implements MistControlTransport<MistMetadataCommand>, MistMetadataTransport
{
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

  send(cmd: MistMetadataCommand): boolean {
    return this.base.send(cmd as any);
  }

  addSendDecorator(d: MistSendDecorator<MistMetadataCommand>): () => void {
    return this.base.addSendDecorator(d as MistSendDecorator<any>);
  }

  addSendListener(l: MistSendListener<MistMetadataCommand>): () => void {
    return this.base.addSendListener(l as MistSendListener<any>);
  }

  once<T extends MistEvent["type"]>(type: T): MistOnceHandle<Extract<MistEvent, { type: T }>> {
    return this.base.once(type);
  }
}
