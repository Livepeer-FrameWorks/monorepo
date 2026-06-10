import { vi } from "vitest";

type Listener = (event: Event) => void;

/**
 * Minimal SourceBuffer fake. appendBuffer/remove flip `updating` true and record
 * the call; the test drives completion explicitly via fireUpdateEnd() so the
 * append-queue draining in SourceBufferManager is deterministic (no timers).
 */
export class FakeSourceBuffer {
  mode = "";
  updating = false;
  readonly appended: ArrayBuffer[] = [];
  readonly removed: Array<[number, number]> = [];
  aborted = false;
  private readonly listeners = new Map<string, Set<Listener>>();

  appendBuffer(data: ArrayBuffer): void {
    this.updating = true;
    this.appended.push(data);
  }

  remove(start: number, end: number): void {
    this.updating = true;
    this.removed.push([start, end]);
  }

  abort(): void {
    this.aborted = true;
  }

  addEventListener(type: string, cb: Listener): void {
    let set = this.listeners.get(type);
    if (!set) this.listeners.set(type, (set = new Set()));
    set.add(cb);
  }

  removeEventListener(type: string, cb: Listener): void {
    this.listeners.get(type)?.delete(cb);
  }

  dispatchEvent(event: Event): boolean {
    for (const cb of this.listeners.get(event.type) ?? []) cb(event);
    return true;
  }

  /** Test helper: resolve the in-flight append/remove and fire updateend. */
  fireUpdateEnd(): void {
    this.updating = false;
    this.dispatchEvent(new Event("updateend"));
  }

  fireError(): void {
    this.dispatchEvent(new Event("error"));
  }
}

export class FakeMediaSource {
  readyState: "open" | "closed" | "ended" = "open";
  duration = NaN;
  readonly buffers: FakeSourceBuffer[] = [];
  addSourceBuffer = vi.fn((_mime: string) => {
    const sb = new FakeSourceBuffer();
    this.buffers.push(sb);
    return sb as unknown as SourceBuffer;
  });
  removeSourceBuffer = vi.fn((_sb: SourceBuffer) => {});
  endOfStream = vi.fn();
}

export interface FakeBufferedRanges {
  length: number;
  start: (i: number) => number;
  end: (i: number) => number;
}

export function makeBuffered(ranges: Array<[number, number]> = []): FakeBufferedRanges {
  return {
    length: ranges.length,
    start: (i: number) => ranges[i][0],
    end: (i: number) => ranges[i][1],
  };
}

/** Minimal HTMLVideoElement stand-in for MSE manager tests. */
export function makeFakeVideo(
  props: Partial<{ currentTime: number; error: unknown; buffered: FakeBufferedRanges }> = {}
) {
  const listeners = new Map<string, Set<Listener>>();
  return {
    currentTime: 0,
    error: null,
    buffered: makeBuffered(),
    addEventListener: vi.fn((type: string, cb: Listener) => {
      let set = listeners.get(type);
      if (!set) listeners.set(type, (set = new Set()));
      set.add(cb);
    }),
    removeEventListener: vi.fn((type: string, cb: Listener) => {
      listeners.get(type)?.delete(cb);
    }),
    _fire(type: string) {
      for (const cb of listeners.get(type) ?? []) cb(new Event(type));
    },
    ...props,
  };
}

/**
 * Install a global `MediaSource` carrying a controllable isTypeSupported.
 * Returns the spy so tests can assert / re-stub. Caller must vi.unstubAllGlobals().
 */
export function stubGlobalMediaSource(isTypeSupported: (mime: string) => boolean = () => true) {
  const spy = vi.fn(isTypeSupported);
  vi.stubGlobal(
    "MediaSource",
    Object.assign(function () {}, { isTypeSupported: spy })
  );
  return spy;
}
