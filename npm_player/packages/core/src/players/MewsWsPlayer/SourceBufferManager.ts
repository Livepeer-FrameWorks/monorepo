/**
 * SourceBuffer Manager for MEWS Player
 *
 * Handles MediaSource Extension (MSE) buffer operations.
 * Ported from reference: mews.js:206-384
 *
 * Key features:
 * - Buffer creation and codec configuration
 * - Append queue management with _append/_do/_doNext pattern
 * - QuotaExceededError handling
 * - Buffer cleanup (_clean)
 * - Track switch msgqueue handling
 */

import type { SourceBufferManagerOptions } from './types';

export class SourceBufferManager {
  private mediaSource: MediaSource;
  private videoElement: HTMLVideoElement;
  private sourceBuffer: SourceBuffer | null = null;
  private onError: (message: string) => void;

  // Append queue for when buffer is busy (ported from mews.js:218)
  private queue: Uint8Array[] = [];

  // Busy flag to prevent concurrent appends (ported from mews.js:296, 266, 301)
  private _busy = false;

  // Operations to run after updateend (ported from mews.js:219, 247-262)
  private do_on_updateend: Array<(remaining?: Array<() => void>) => void> = [];

  // Message queue for track switches (ported from mews.js:452, 689-693)
  // Array of arrays to handle rapid switches. Binary data goes to latest array.
  // On reinit, oldest array is drained first to preserve order.
  private msgqueue: Uint8Array[][] | false = false;

  // Track current codecs to skip unnecessary reinits
  private _codecs: string[] = [];

  // Fragment counter for proactive buffer cleaning (ported from mews.js:223, 237-245)
  private fragmentCount = 0;

  // Paused flag for browser pause detection (ported from mews.js:506, 791)
  public paused = false;

  // Debugging mode
  private debugging = false;

  constructor(options: SourceBufferManagerOptions) {
    this.mediaSource = options.mediaSource;
    this.videoElement = options.videoElement;
    this.onError = options.onError;
  }

  /**
   * Initialize the SourceBuffer with the given codecs.
   * Ported from mews.js:206-384 (sbinit function)
   */
  initWithCodecs(codecs: string[]): boolean {
    if (this.sourceBuffer) {
      // Already initialized
      return true;
    }
    if (!codecs || !codecs.length) {
      this.onError('No codecs provided');
      return false;
    }

    const container = 'mp4'; // Could be 'webm' for WebM container
    const mime = `video/${container};codecs="${codecs.join(',')}"`;

    if (!MediaSource.isTypeSupported(mime)) {
      this.onError(`Unsupported MSE codec: ${mime}`);
      return false;
    }

    try {
      // Create SourceBuffer (mews.js:211)
      this.sourceBuffer = this.mediaSource.addSourceBuffer(mime);
      // Use segments mode - fragments will be put at correct time (mews.js:212)
      this.sourceBuffer.mode = 'segments';
      // Save current codecs (mews.js:215)
      this._codecs = codecs.slice();

      this.installEventHandlers();

      // Drain any pre-buffered messages from track switch queue (mews.js:341-367)
      this.drainMessageQueue();

      // Flush any data that was queued before sourceBuffer was ready
      this.flushQueue();

      return true;
    } catch (e: any) {
      this.onError(e?.message || 'Failed to create SourceBuffer');
      return false;
    }
  }

  /**
   * Get current codecs
   */
  getCodecs(): string[] {
    return this._codecs;
  }

  /**
   * Queue data for appending to the buffer.
   * If a track switch is in progress (msgqueue active), data goes to the latest queue.
   * Ported from mews.js:809-824
   */
  append(data: Uint8Array): void {
    if (!data || !data.byteLength) return;

    // If track switch in progress, queue to msgqueue instead (mews.js:818-824)
    if (this.msgqueue) {
      this.msgqueue[this.msgqueue.length - 1].push(data);
      return;
    }

    // No sourceBuffer yet, queue for later (mews.js:809-816)
    if (!this.sourceBuffer) {
      this.queue.push(data);
      return;
    }

    // Check if we can append now
    if (this.sourceBuffer.updating || this.queue.length || this._busy) {
      this.queue.push(data);
      return;
    }

    // Append directly
    this._append(data);
  }

  /**
   * Internal append with error handling.
   * Ported from mews.js:292-339
   */
  private _append(data: Uint8Array): void {
    if (!data || !data.buffer) return;
    if (!this.sourceBuffer) return;

    if (this._busy) {
      // Still busy, put back in queue (mews.js:296-300)
      if (this.debugging) console.warn('MEWS: wanted to append but busy, requeuing');
      this.queue.unshift(data);
      return;
    }

    this._busy = true;

    try {
      // Handle SharedArrayBuffer edge case (mews.js doesn't have this, but we need it)
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
      // Error handling (mews.js:306-338)
      switch (e?.name) {
        case 'QuotaExceededError': {
          // Buffer is full - clean up and retry (mews.js:308-324)
          const buffered = this.videoElement.buffered;
          if (buffered.length && this.videoElement.currentTime - buffered.start(0) > 1) {
            // Clear as much from buffer as we can
            if (this.debugging) {
              console.log('MEWS: QuotaExceededError, cleaning buffer');
            }
            this._clean(1); // Keep 1 second
            this._busy = false;
            this._append(data); // Retry
            return;
          } else if (buffered.length) {
            // Can't clean more, skip ahead (mews.js:316-319)
            const bufferEnd = buffered.end(buffered.length - 1);
            if (this.debugging) {
              console.log('MEWS: QuotaExceededError, skipping ahead');
            }
            this.videoElement.currentTime = bufferEnd;
            this._busy = false;
            this._append(data);
            return;
          }
          break;
        }
        case 'InvalidStateError': {
          // Playback is borked (mews.js:326-334)
          if (this.videoElement.error) {
            // Video element error will handle this
            this._busy = false;
            return;
          }
          break;
        }
      }
      this.onError(e?.message || 'Append buffer failed');
      this._busy = false;
    }
  }

  /**
   * Schedule an operation to run on next updateend.
   * Ported from mews.js:281-283
   */
  _doNext(func: () => void): void {
    this.do_on_updateend.push(func);
  }

  /**
   * Run operation now if not busy, otherwise schedule for updateend.
   * Ported from mews.js:284-291
   */
  _do(func: (remaining?: Array<() => void>) => void): void {
    if (!this.sourceBuffer) {
      this._doNext(func);
      return;
    }
    if (this.sourceBuffer.updating || this._busy) {
      this._doNext(func);
    } else {
      func();
    }
  }

  /**
   * Schedule an operation to run after the current SourceBuffer update completes.
   * Public API for external callers.
   */
  scheduleAfterUpdate(fn: () => void): void {
    this._doNext(fn);
  }

  /**
   * Change codecs mid-stream (track switch).
   * Uses message queue to prevent data loss during rapid switches.
   * Ported from mews.js:623-788
   */
  changeCodecs(codecs: string[], switchPointMs?: number): void {
    // Skip reinit if codecs are identical (mews.js:676)
    if (this.codecsEqual(this._codecs, codecs)) {
      if (this.debugging) console.log('MEWS: keeping source buffer, codecs same');
      return;
    }

    const container = 'mp4';
    const mime = `video/${container};codecs="${codecs.join(',')}"`;
    if (!MediaSource.isTypeSupported(mime)) {
      this.onError(`Unsupported codec for switch: ${mime}`);
      return;
    }

    // Start message queue for track switch (mews.js:689-693)
    if (this.msgqueue) {
      this.msgqueue.push([]); // Add new queue for rapid switch
    } else {
      this.msgqueue = [[]];
    }

    const pendingCodecs = codecs.slice();

    if (typeof switchPointMs === 'number' && switchPointMs > 0) {
      // Wait for playback to reach switching point (mews.js:751-785)
      this.awaitSwitchingPoint(mime, switchPointMs, pendingCodecs);
    } else {
      // Clear and reinit immediately
      this.clearAndReinit(mime, pendingCodecs);
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
   * Find which buffer range contains the given position.
   * Ported from mews.js:947-956
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

  /**
   * Remove buffer content before keepaway seconds from current position.
   * Ported from mews.js:370-379
   */
  _clean(keepaway: number = 180): void {
    if (!this.sourceBuffer) return;

    const currentTime = this.videoElement.currentTime;
    if (currentTime <= keepaway) return;

    this._do(() => {
      if (!this.sourceBuffer) return;
      try {
        // Make sure end time is never 0 (mews.js:376)
        this.sourceBuffer.remove(0, Math.max(0.1, currentTime - keepaway));
      } catch (e) {
        // Ignore errors during cleanup
      }
    });
  }

  /**
   * Clear buffer and reinitialize with new codecs.
   * Ported from mews.js:696-730
   */
  private clearAndReinit(mime: string, newCodecs: string[]): void {
    this._do((remaining_do_on_updateend) => {
      if (!this.sourceBuffer) {
        // No sourceBuffer to clear, just reinit
        this.reinitBuffer(mime, newCodecs, remaining_do_on_updateend);
        return;
      }

      if (this.sourceBuffer.updating) {
        // Still updating, schedule for later
        this._doNext(() => this.clearAndReinit(mime, newCodecs));
        return;
      }

      try {
        // Clear buffer (mews.js:701)
        if (!isNaN(this.mediaSource.duration)) {
          this.sourceBuffer.remove(0, Infinity);
        }
      } catch (e) {
        // Ignore
      }

      // Wait for remove to complete, then reinit
      this._doNext(() => {
        this.reinitBuffer(mime, newCodecs, remaining_do_on_updateend);
      });
    });
  }

  /**
   * Reinitialize buffer with new codecs.
   * Ported from mews.js:703-724
   */
  private reinitBuffer(
    mime: string,
    newCodecs: string[],
    remaining_do_on_updateend?: Array<() => void>
  ): void {
    // Save queue
    const remaining = this.queue.slice();
    this.queue = [];

    // Remove old sourceBuffer
    if (this.sourceBuffer && this.mediaSource.readyState === 'open') {
      try {
        this.mediaSource.removeSourceBuffer(this.sourceBuffer);
      } catch (e) {
        // Ignore
      }
    }
    this.sourceBuffer = null;
    this._busy = false;

    try {
      // Create new sourceBuffer (mews.js:713-715)
      this.sourceBuffer = this.mediaSource.addSourceBuffer(mime);
      this.sourceBuffer.mode = 'segments';
      this._codecs = newCodecs;

      this.installEventHandlers();

      // Restore any remaining do_on_updateend functions (mews.js:715)
      if (remaining_do_on_updateend?.length) {
        this.do_on_updateend = remaining_do_on_updateend;
      }

      // Drain message queue (mews.js:341-367)
      this.drainMessageQueue();

      // Restore queued data
      for (const frag of remaining) {
        this.append(frag);
      }
    } catch (e: any) {
      this.onError(e?.message || 'Failed to reinit SourceBuffer');
    }
  }

  /**
   * Wait for playback to reach switch point, then clear and reinit.
   * Ported from mews.js:751-785
   */
  private awaitSwitchingPoint(mime: string, switchPointMs: number, newCodecs: string[]): void {
    const tSec = switchPointMs / 1000;

    const clearAndReinit = () => {
      this.clearAndReinit(mime, newCodecs);
    };

    // Wait for video.currentTime to reach switch point
    const onTimeUpdate = () => {
      if (this.videoElement.currentTime >= tSec) {
        this.videoElement.removeEventListener('timeupdate', onTimeUpdate);
        this.videoElement.removeEventListener('waiting', onWaiting);
        clearAndReinit();
      }
    };

    // Or if video is waiting (buffer empty before switch point)
    const onWaiting = () => {
      this.videoElement.removeEventListener('timeupdate', onTimeUpdate);
      this.videoElement.removeEventListener('waiting', onWaiting);
      clearAndReinit();
    };

    this.videoElement.addEventListener('timeupdate', onTimeUpdate);
    this.videoElement.addEventListener('waiting', onWaiting);
  }

  /**
   * Drain the oldest message queue and append its data.
   * Called after source buffer reinit to flush queued track switch data.
   * Ported from mews.js:341-367
   */
  private drainMessageQueue(): void {
    if (!this.msgqueue || this.msgqueue.length === 0) return;

    // Get oldest queue
    const oldest = this.msgqueue[0];

    let do_do = false; // If no messages, trigger updateend manually (mews.js:357-358)

    if (oldest.length) {
      // Append all data from oldest queue (mews.js:346-355)
      for (const frag of oldest) {
        if (this.sourceBuffer && (this.sourceBuffer.updating || this.queue.length || this._busy)) {
          this.queue.push(frag);
        } else {
          this._append(frag);
        }
      }
    } else {
      do_do = true;
    }

    // Remove oldest queue (mews.js:360)
    this.msgqueue.shift();
    if (this.msgqueue.length === 0) {
      this.msgqueue = false;
    }

    if (this.debugging) {
      console.log(
        'MEWS: drained msgqueue',
        this.msgqueue ? `${this.msgqueue.length} more queue(s) remain` : ''
      );
    }

    // Manually trigger updateend if queue was empty (mews.js:363-366)
    if (do_do && this.sourceBuffer) {
      this.sourceBuffer.dispatchEvent(new Event('updateend'));
    }
  }

  /**
   * Install event handlers on the sourceBuffer.
   * Ported from mews.js:224-279
   */
  private installEventHandlers(): void {
    if (!this.sourceBuffer) return;

    this.sourceBuffer.addEventListener('updateend', () => {
      if (!this.sourceBuffer) {
        if (this.debugging) console.log('MEWS: updateend but sourceBuffer is null');
        return;
      }

      // Every 500 fragments, clean the buffer (mews.js:237-245)
      if (this.fragmentCount >= 500) {
        this.fragmentCount = 0;
        this._clean(10); // Keep 10 seconds
      } else {
        this.fragmentCount++;
      }

      // Execute queued operations (mews.js:247-262)
      const do_funcs = this.do_on_updateend.slice();
      this.do_on_updateend = [];

      for (let i = 0; i < do_funcs.length; i++) {
        if (!this.sourceBuffer) {
          if (this.debugging) console.warn('MEWS: doing updateend but sb was reset');
          break;
        }
        if (this.sourceBuffer.updating) {
          // Still updating, requeue remaining functions (mews.js:255-259)
          this.do_on_updateend = this.do_on_updateend.concat(do_funcs.slice(i));
          if (this.debugging) console.warn('MEWS: doing updateend but was interrupted');
          break;
        }
        try {
          // Pass remaining functions as argument (mews.js:261)
          do_funcs[i](i < do_funcs.length - 1 ? do_funcs.slice(i + 1) : []);
        } catch (e) {
          console.error('MEWS: error in do_on_updateend:', e);
        }
      }

      this._busy = false;

      // Process queued data (mews.js:269-272)
      if (this.sourceBuffer && this.queue.length > 0 && !this.sourceBuffer.updating && !this.videoElement.error) {
        this._append(this.queue.shift()!);
      }
    });

    this.sourceBuffer.addEventListener('error', () => {
      this.onError('SourceBuffer error');
    });
  }

  /**
   * Flush any pending queue data.
   */
  private flushQueue(): void {
    if (!this.sourceBuffer) return;

    const pending = this.queue.slice();
    this.queue = [];
    for (const frag of pending) {
      this.append(frag);
    }
  }

  /**
   * Check if there's an active message queue (track switch in progress)
   */
  hasActiveMessageQueue(): boolean {
    return this.msgqueue !== false;
  }

  destroy(): void {
    if (this.sourceBuffer) {
      try {
        this.sourceBuffer.abort();
      } catch {}
    }
    this.sourceBuffer = null;
    this.queue = [];
    this._busy = false;
    this.do_on_updateend = [];
    this.msgqueue = false;
    this.paused = false;
  }
}
