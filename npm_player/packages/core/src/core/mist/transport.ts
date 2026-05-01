import type { MistCommand, MistEvent, MistMetadataCommand } from "./protocol";

export type MistTransportState =
  | "disconnected"
  | "connecting"
  | "connected"
  | "reconnecting"
  | "closed";

export interface MistTransportEvents {
  statechange: { state: MistTransportState; code?: number };
  event: { event: MistEvent };
  binary: { data: ArrayBuffer };
  error: { message: string; cause?: unknown };
}

export type MistSendDecorator<C> = (cmd: C) => C | null;
export type MistSendListener<C> = (cmd: C) => void;

export interface MistOnceHandle<T> {
  promise: Promise<T>;
  cancel(reason?: string): void;
}

export interface MistControlTransport<C = MistCommand> {
  readonly state: MistTransportState;
  readonly capabilities: { binary: boolean; reconnect: boolean };

  on<K extends keyof MistTransportEvents>(
    event: K,
    listener: (data: MistTransportEvents[K]) => void
  ): () => void;
  off<K extends keyof MistTransportEvents>(
    event: K,
    listener: (data: MistTransportEvents[K]) => void
  ): void;

  send(cmd: C): boolean;
  addSendDecorator(d: MistSendDecorator<C>): () => void;
  addSendListener(l: MistSendListener<C>): () => void;

  once<T extends MistEvent["type"]>(type: T): MistOnceHandle<Extract<MistEvent, { type: T }>>;

  connect(): Promise<void>;
  disconnect(reason?: string): void;
  destroy(): void;
}

export type MistMediaTransport = MistControlTransport<MistCommand>;
export type MistMetadataTransport = MistControlTransport<MistMetadataCommand>;
