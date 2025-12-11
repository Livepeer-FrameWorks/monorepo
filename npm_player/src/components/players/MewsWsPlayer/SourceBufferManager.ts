/**
 * SourceBuffer Manager for MEWS Player
 *
 * Handles MediaSource Extension (MSE) buffer operations:
 * - Buffer creation and codec configuration
 * - Append queue management
 * - QuotaExceededError handling
 * - Buffer cleanup
 */

import type { SourceBufferManagerOptions } from './types';

export class SourceBufferManager {
  private mediaSource: MediaSource;
  private videoElement: HTMLVideoElement;
  private sourceBuffer: SourceBuffer | null = null;
  private queue: Uint8Array[] = [];
  private busy = false;
  private pendingOperations: Array<() => void> = [];
  private onError: (message: string) => void;

  // Message queue for track switches - array of arrays to handle rapid switches
  // When codecs change, a new inner array is added. Binary data goes to latest array.
  // On reinit, oldest array is drained first to preserve order.
  private msgqueue: Uint8Array[][] | false = false;

  // Track current codecs to skip unnecessary reinits
  private currentCodecs: string[] = [];

  // Fragment counter for proactive buffer cleaning
  private fragmentCount = 0;

  constructor(options: SourceBufferManagerOptions) {
    this.mediaSource = options.mediaSource;
    this.videoElement = options.videoElement;
    this.onError = options.onError;
  }

  /**
   * Initialize the SourceBuffer with the given codecs
   */
  initWithCodecs(codecs: string[]): boolean {
    if (this.sourceBuffer) return true;
    if (!codecs.length) {
      this.onError('No codecs provided');
      return false;
    }

    const mime = `video/mp4; codecs="${codecs.join(',')}"`;
    if (!MediaSource.isTypeSupported(mime)) {
      this.onError(`Unsupported MSE codec: ${mime}`);
      return false;
    }

    try {
      this.sourceBuffer = this.mediaSource.addSourceBuffer(mime);
      this.sourceBuffer.mode = 'segments';
      this.currentCodecs = codecs.slice();
      this.installEventHandlers();
      this.drainMessageQueue();
      this.flushQueue();
      return true;
    } catch (e: any) {
      this.onError(e?.message || 'Failed to create SourceBuffer');
      return false;
    }
  }

  /**
   * Queue data for appending to the buffer.
   * If a track switch is in progress (msgqueue active), data goes to the latest queue.
   */
  append(data: Uint8Array): void {
    if (!data || !data.byteLength) return;

    // If track switch in progress, queue to msgqueue instead
    if (this.msgqueue) {
      this.msgqueue[this.msgqueue.length - 1].push(data);
      return;
    }

    if (!this.sourceBuffer) {
      this.queue.push(data);
      return;
    }

    if (this.sourceBuffer.updating || this.busy) {
      this.queue.push(data);
      return;
    }

    this.doAppend(data);
  }

  /**
   * Change codecs mid-stream (track switch).
   * Uses message queue to prevent data loss during rapid switches.
   * Skips reinit if codecs are identical (3.3.3).
   */
  changeCodecs(codecs: string[], switchPointMs?: number): void {
    // Skip reinit if codecs are identical
    if (this.codecsEqual(this.currentCodecs, codecs)) {
      return;
    }

    const mime = `video/mp4; codecs="${codecs.join(',')}"`;
    if (!MediaSource.isTypeSupported(mime)) return;

    // Start message queue for track switch
    if (this.msgqueue) {
      this.msgqueue.push([]); // Add new queue for rapid switch
    } else {
      this.msgqueue = [[]];
    }

    // Store new codecs (will be applied on reinit)
    const pendingCodecs = codecs.slice();

    if (typeof switchPointMs === 'number') {
      this.awaitSwitchingPoint(mime, switchPointMs, pendingCodecs);
    } else {
      this.reinitBuffer(mime, pendingCodecs);
    }
  }

  /**
   * Check if two codec arrays are equivalent (order-independent)
   */
  private codecsEqual(arr1: string[], arr2: string[]): boolean {
    if (arr1.length !== arr2.length) return false;
    for (const codec of arr1) {
      if (!arr2.includes(codec)) return false;
    }
    return true;
  }

  /**
   * Schedule an operation after the current update completes
   */
  scheduleAfterUpdate(fn: () => void): void {
    if (!this.sourceBuffer || this.sourceBuffer.updating || this.busy) {
      this.pendingOperations.push(fn);
    } else {
      fn();
    }
  }

  /**
   * Find which buffer range contains the given position
   */
  findBufferIndex(position: number): number | false {
    const buffered = this.videoElement.buffered;
    for (let i = 0; i < buffered.length; i++) {
      if (buffered.start(i) <= position && buffered.end(i) >= position) {
        return i;
      }
    }
    return false;
  }

  destroy(): void {
    if (this.sourceBuffer) {
      try {
        this.sourceBuffer.abort();
      } catch {}
    }
    this.sourceBuffer = null;
    this.queue = [];
    this.busy = false;
    this.pendingOperations = [];
  }

  private installEventHandlers(): void {
    if (!this.sourceBuffer) return;

    this.sourceBuffer.addEventListener('updateend', () => {
      this.busy = false;
      if (!this.sourceBuffer || this.sourceBuffer.updating) return;

      // Proactive buffer cleaning every 500 fragments (3.3.5)
      this.fragmentCount++;
      if (this.fragmentCount >= 500) {
        this.fragmentCount = 0;
        this.cleanBuffer(10);
      }

      // Process queued data
      const next = this.queue.shift();
      if (next) {
        this.doAppend(next);
      }

      // Run scheduled operations
      const ops = this.pendingOperations.slice();
      this.pendingOperations = [];
      for (const fn of ops) {
        try {
          fn();
        } catch {}
      }
    });

    this.sourceBuffer.addEventListener('error', () => {
      this.onError('SourceBuffer error');
    });
  }

  private doAppend(data: Uint8Array): void {
    if (!this.sourceBuffer) return;

    try {
      this.busy = true;

      // Handle SharedArrayBuffer (rare case)
      if (data.buffer instanceof SharedArrayBuffer) {
        const buffer = new ArrayBuffer(data.byteLength);
        new Uint8Array(buffer).set(data);
        this.sourceBuffer.appendBuffer(buffer);
      } else {
        this.sourceBuffer.appendBuffer(
          data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength)
        );
      }
    } catch (e: any) {
      if (e?.name === 'QuotaExceededError') {
        this.cleanBuffer(10);
        this.busy = false;
        this.queue.unshift(data);
        return;
      }
      this.onError(e?.message || 'Append buffer failed');
      this.busy = false;
    }
  }

  private flushQueue(): void {
    const pending = this.queue.slice();
    this.queue = [];
    for (const frag of pending) {
      this.append(frag);
    }
  }

  private cleanBuffer(keepAwaySeconds: number): void {
    if (!this.sourceBuffer) return;

    const buffered = this.videoElement.buffered;
    if (!buffered || buffered.length === 0) return;

    const end = this.videoElement.currentTime - Math.max(0.1, keepAwaySeconds);
    if (end <= 0) return;

    try {
      this.sourceBuffer.remove(0, end);
    } catch {}
  }

  private reinitBuffer(mime: string, newCodecs?: string[]): void {
    const remaining = this.queue;
    this.queue = [];

    if (this.sourceBuffer && this.mediaSource.readyState === 'open') {
      try {
        this.mediaSource.removeSourceBuffer(this.sourceBuffer);
      } catch {}
    }

    this.sourceBuffer = this.mediaSource.addSourceBuffer(mime);
    this.sourceBuffer.mode = 'segments';
    if (newCodecs) {
      this.currentCodecs = newCodecs;
    }
    this.installEventHandlers();

    // Drain message queue (oldest first for track switches)
    this.drainMessageQueue();

    for (const frag of remaining) {
      this.append(frag);
    }
  }

  /**
   * Drain the oldest message queue and append its data.
   * Called after source buffer reinit to flush queued track switch data.
   */
  private drainMessageQueue(): void {
    if (!this.msgqueue || this.msgqueue.length === 0) return;

    // Drain oldest queue
    const oldest = this.msgqueue.shift()!;
    for (const frag of oldest) {
      this.queue.push(frag);
    }

    // If no more queues, disable msgqueue mode
    if (this.msgqueue.length === 0) {
      this.msgqueue = false;
    }
  }

  private awaitSwitchingPoint(mime: string, switchPointMs: number, newCodecs?: string[]): void {
    const tSec = switchPointMs / 1000;

    const clearAndReinit = () => {
      this.scheduleAfterUpdate(() => {
        this.reinitBuffer(mime, newCodecs);
      });
    };

    const onTimeUpdate = () => {
      if (this.videoElement.currentTime >= tSec) {
        this.videoElement.removeEventListener('timeupdate', onTimeUpdate);
        this.videoElement.removeEventListener('waiting', onWaiting);
        clearAndReinit();
      }
    };

    const onWaiting = () => {
      this.videoElement.removeEventListener('timeupdate', onTimeUpdate);
      this.videoElement.removeEventListener('waiting', onWaiting);
      clearAndReinit();
    };

    this.videoElement.addEventListener('timeupdate', onTimeUpdate);
    this.videoElement.addEventListener('waiting', onWaiting);
  }
}
