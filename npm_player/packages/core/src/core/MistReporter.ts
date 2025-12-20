/**
 * MistReporter - Reports playback stats to MistServer
 *
 * Implements the same reporting protocol as MistPlayer reference:
 * - Sends initial report on player selection (player, sourceType, sourceUrl, pageUrl)
 * - Reports deltas every 5 seconds
 * - Tracks waiting/stalled events and durations
 * - Integrates with QualityMonitor for playbackScore
 * - Sends final report on unload
 *
 * Reports are sent over the same WebSocket used for stream state.
 */

import { TimerManager } from './TimerManager';

export interface MistReporterStats {
  nWaiting: number;
  timeWaiting: number;
  nStalled: number;
  timeStalled: number;
  timeUnpaused: number;
  nError: number;
  lastError: string | null;
  firstPlayback: number | null;
  playbackScore: number;
  autoplay: 'success' | 'muted' | 'failed' | null;
  videoHeight: number | null;
  videoWidth: number | null;
  playerHeight: number | null;
  playerWidth: number | null;
  tracks: string | null;
  nLog: number;
}

export interface MistReporterInitialReport {
  player: string;
  sourceType: string;
  sourceUrl: string;
  pageUrl: string;
}

export interface MistReporterOptions {
  /** WebSocket to send reports through (shared with stream state) */
  socket?: WebSocket | null;
  /** Report interval in ms (default: 5000) */
  reportInterval?: number;
  /** E2: Batch flush interval in ms (default: 1000) - max rate for non-critical reports */
  batchFlushInterval?: number;
  /** Boot timestamp for firstPlayback calculation */
  bootMs?: number;
  /** Log array reference for including logs in reports */
  logs?: string[];
}

type StatsKey = keyof MistReporterStats;

/**
 * MistReporter - Playback telemetry to MistServer
 */
export class MistReporter {
  private socket: WebSocket | null = null;
  private videoElement: HTMLVideoElement | null = null;
  private containerElement: HTMLElement | null = null;
  private reportInterval: number;
  private batchFlushInterval: number;
  private bootMs: number;
  private logs: string[] = [];

  // Internal stats storage (different structure for time tracking)
  private _stats: {
    _nWaiting: number;
    _nStalled: number;
    _nError: number;
    lastError: string | null;
    firstPlayback: number | null;
    playbackScore: number;
    autoplay: 'success' | 'muted' | 'failed' | null;
    tracks: string | null;
  };

  // Time tracking state
  private waitingSince: number = 0;
  private stalledSince: number = 0;
  private unpausedSince: number = 0;
  private timeWaitingAccum: number = 0;
  private timeStalledAccum: number = 0;
  private timeUnpausedAccum: number = 0;

  // Last reported values for delta detection
  private lastReported: Partial<MistReporterStats> = {};

  // Timer manager for periodic reporting
  private timers = new TimerManager();

  // Event listener cleanup functions
  private listeners: Array<() => void> = [];

  // Track if first playback has been recorded
  private firstPlaybackRecorded = false;

  // E2: Batch reporting state
  private pendingBatch: Record<string, unknown> = {};
  private hasPendingBatch = false;
  private lastFlushTime = 0;
  private batchFlushTimerId: number | null = null;

  // E3: Offline queue for telemetry
  private offlineQueue: Record<string, unknown>[] = [];
  private static readonly MAX_OFFLINE_QUEUE_SIZE = 100;

  constructor(options: MistReporterOptions = {}) {
    this.socket = options.socket ?? null;
    this.reportInterval = options.reportInterval ?? 5000;
    this.batchFlushInterval = options.batchFlushInterval ?? 1000;
    this.bootMs = options.bootMs ?? Date.now();
    this.logs = options.logs ?? [];

    // Initialize stats
    this._stats = {
      _nWaiting: 0,
      _nStalled: 0,
      _nError: 0,
      lastError: null,
      firstPlayback: null,
      playbackScore: 1.0,
      autoplay: null,
      tracks: null,
    };

    // Initialize lastReported to empty
    this.lastReported = {
      nWaiting: 0,
      timeWaiting: 0,
      nStalled: 0,
      timeStalled: 0,
      timeUnpaused: 0,
      nError: 0,
      lastError: null,
      firstPlayback: null,
      playbackScore: 1,
      autoplay: null,
      videoHeight: null,
      videoWidth: null,
      playerHeight: null,
      playerWidth: null,
      tracks: null,
      nLog: 0,
    };
  }

  /**
   * Set the WebSocket to use for reporting
   * E3: Flushes offline queue when socket becomes available
   */
  setSocket(socket: WebSocket | null): void {
    const wasDisconnected = !this.socket || this.socket.readyState !== WebSocket.OPEN;
    this.socket = socket;

    // E3: Flush offline queue when reconnecting
    if (wasDisconnected && socket?.readyState === WebSocket.OPEN) {
      this.flushOfflineQueue();
    }
  }

  /**
   * E3: Flush queued reports that were collected while offline
   */
  private flushOfflineQueue(): void {
    if (this.offlineQueue.length === 0) {
      return;
    }

    // Send all queued reports
    for (const report of this.offlineQueue) {
      this.sendReport(report);
    }

    this.offlineQueue = [];
  }

  /**
   * Get current stats object with computed getters
   * E1: Uses performance.now() for sub-millisecond precision in duration tracking
   */
  getStats(): MistReporterStats {
    const now = performance.now();

    return {
      nWaiting: this._stats._nWaiting,
      timeWaiting: Math.round(this.timeWaitingAccum + (this.waitingSince > 0 ? now - this.waitingSince : 0)),
      nStalled: this._stats._nStalled,
      timeStalled: Math.round(this.timeStalledAccum + (this.stalledSince > 0 ? now - this.stalledSince : 0)),
      timeUnpaused: Math.round(this.timeUnpausedAccum + (this.unpausedSince > 0 ? now - this.unpausedSince : 0)),
      nError: this._stats._nError,
      lastError: this._stats.lastError,
      firstPlayback: this._stats.firstPlayback,
      playbackScore: Math.round(this._stats.playbackScore * 10) / 10,
      autoplay: this._stats.autoplay,
      videoHeight: this.videoElement?.videoHeight ?? null,
      videoWidth: this.videoElement?.videoWidth ?? null,
      playerHeight: this.containerElement?.clientHeight ?? null,
      playerWidth: this.containerElement?.clientWidth ?? null,
      tracks: this._stats.tracks,
      nLog: this.logs.length,
    };
  }

  /**
   * Set a stat value
   */
  set<K extends StatsKey>(key: K, value: MistReporterStats[K]): void {
    if (key === 'nWaiting') {
      this._stats._nWaiting = value as number;
    } else if (key === 'nStalled') {
      this._stats._nStalled = value as number;
    } else if (key === 'nError') {
      this._stats._nError = value as number;
    } else if (key === 'lastError') {
      this._stats.lastError = value as string | null;
    } else if (key === 'firstPlayback') {
      this._stats.firstPlayback = value as number | null;
    } else if (key === 'playbackScore') {
      this._stats.playbackScore = value as number;
    } else if (key === 'autoplay') {
      this._stats.autoplay = value as 'success' | 'muted' | 'failed' | null;
    } else if (key === 'tracks') {
      this._stats.tracks = value as string | null;
    }
    // Other keys (computed) are read-only
  }

  /**
   * Increment a counter stat
   */
  add(key: 'nWaiting' | 'nStalled' | 'nError', amount = 1): void {
    if (key === 'nWaiting') {
      this._stats._nWaiting += amount;
    } else if (key === 'nStalled') {
      this._stats._nStalled += amount;
    } else if (key === 'nError') {
      this._stats._nError += amount;
    }
  }

  /**
   * Initialize reporting for a video element
   */
  init(videoElement: HTMLVideoElement, containerElement?: HTMLElement): void {
    this.videoElement = videoElement;
    this.containerElement = containerElement ?? videoElement.parentElement ?? null;
    this.firstPlaybackRecorded = false;

    // Set up event listeners like MistPlayer reference
    // E1: Use performance.now() for sub-millisecond precision in duration tracking
    const onPlaying = () => {
      const now = performance.now();
      const isFirstPlay = !this.firstPlaybackRecorded;

      // Record first playback time (still uses Date.now for absolute timestamp)
      if (isFirstPlay) {
        this._stats.firstPlayback = Date.now() - this.bootMs;
        this.firstPlaybackRecorded = true;
      }

      // End waiting state if active
      if (this.waitingSince > 0) {
        this.timeWaitingAccum += now - this.waitingSince;
        this.waitingSince = 0;
      }

      // End stalled state if active
      if (this.stalledSince > 0) {
        this.timeStalledAccum += now - this.stalledSince;
        this.stalledSince = 0;
      }

      // Start unpaused timer
      if (this.unpausedSince === 0) {
        this.unpausedSince = now;
      }

      // E2: Flush immediately on first playback
      if (isFirstPlay) {
        this.reportStats();
        this.flushBatch();
      }
    };

    const onWaiting = () => {
      this._stats._nWaiting++;
      if (this.waitingSince === 0) {
        this.waitingSince = performance.now();
      }
    };

    const onStalled = () => {
      this._stats._nStalled++;
      if (this.stalledSince === 0) {
        this.stalledSince = performance.now();
      }
    };

    const onPause = () => {
      // End unpaused timer
      if (this.unpausedSince > 0) {
        this.timeUnpausedAccum += performance.now() - this.unpausedSince;
        this.unpausedSince = 0;
      }
    };

    const onError = (e: Event) => {
      this._stats._nError++;
      const error = (e as ErrorEvent).message || 'Unknown error';
      this._stats.lastError = error;

      // E2: Flush immediately on error
      this.reportStats();
      this.flushBatch();
    };

    const onCanPlay = () => {
      // End waiting state if active
      if (this.waitingSince > 0) {
        this.timeWaitingAccum += performance.now() - this.waitingSince;
        this.waitingSince = 0;
      }
    };

    videoElement.addEventListener('playing', onPlaying);
    videoElement.addEventListener('waiting', onWaiting);
    videoElement.addEventListener('stalled', onStalled);
    videoElement.addEventListener('pause', onPause);
    videoElement.addEventListener('error', onError);
    videoElement.addEventListener('canplay', onCanPlay);

    this.listeners = [
      () => videoElement.removeEventListener('playing', onPlaying),
      () => videoElement.removeEventListener('waiting', onWaiting),
      () => videoElement.removeEventListener('stalled', onStalled),
      () => videoElement.removeEventListener('pause', onPause),
      () => videoElement.removeEventListener('error', onError),
      () => videoElement.removeEventListener('canplay', onCanPlay),
    ];

    // Start periodic reporting
    this.startReporting();
  }

  /**
   * Send initial report when player is selected
   */
  sendInitialReport(info: MistReporterInitialReport): void {
    this.report(info as unknown as Record<string, unknown>);
  }

  /**
   * Update playback score (call from QualityMonitor)
   */
  setPlaybackScore(score: number): void {
    this._stats.playbackScore = score;
  }

  /**
   * Update autoplay status
   */
  setAutoplayStatus(status: 'success' | 'muted' | 'failed'): void {
    this._stats.autoplay = status;
  }

  /**
   * Update current tracks
   */
  setTracks(tracks: string[]): void {
    this._stats.tracks = tracks.join(',');
  }

  /**
   * Send a report over WebSocket immediately
   * E3: Queues reports when socket is unavailable (up to MAX_OFFLINE_QUEUE_SIZE)
   */
  private sendReport(data: Record<string, unknown>): void {
    // If socket not available, queue for later
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      // E3: Queue report for when socket reconnects
      if (this.offlineQueue.length < MistReporter.MAX_OFFLINE_QUEUE_SIZE) {
        this.offlineQueue.push(data);
      }
      // If queue is full, oldest reports are lost (FIFO overflow)
      return;
    }

    try {
      this.socket.send(JSON.stringify(data));
    } catch {
      // E3: Queue on send failure too
      if (this.offlineQueue.length < MistReporter.MAX_OFFLINE_QUEUE_SIZE) {
        this.offlineQueue.push(data);
      }
    }
  }

  /**
   * E2: Queue data for batched reporting
   * Merges with pending batch, schedules flush if not already pending
   */
  private report(data: Record<string, unknown>): void {
    // Merge into pending batch
    Object.assign(this.pendingBatch, data);
    this.hasPendingBatch = true;

    // Schedule flush if not already scheduled
    if (this.batchFlushTimerId === null) {
      const now = performance.now();
      const timeSinceLastFlush = now - this.lastFlushTime;
      const delay = Math.max(0, this.batchFlushInterval - timeSinceLastFlush);

      this.batchFlushTimerId = this.timers.start(() => {
        this.batchFlushTimerId = null;
        this.flushBatch();
      }, delay, 'batchFlush');
    }
  }

  /**
   * E2: Flush pending batch immediately
   * Used for critical events (error, first play, unload)
   */
  flushBatch(): void {
    if (this.batchFlushTimerId !== null) {
      this.timers.stop(this.batchFlushTimerId);
      this.batchFlushTimerId = null;
    }

    if (!this.hasPendingBatch) {
      return;
    }

    this.sendReport(this.pendingBatch);
    this.pendingBatch = {};
    this.hasPendingBatch = false;
    this.lastFlushTime = performance.now();
  }

  /**
   * Report stats delta (only changed values)
   * E2: Now queues to batch instead of sending immediately
   */
  reportStats(): void {
    const current = this.getStats();
    const delta: Record<string, unknown> = {};
    let hasChanges = false;

    // Compare each stat and include only changed values
    const keys: StatsKey[] = [
      'nWaiting', 'timeWaiting', 'nStalled', 'timeStalled', 'timeUnpaused',
      'nError', 'lastError', 'firstPlayback', 'playbackScore', 'autoplay',
      'videoHeight', 'videoWidth', 'playerHeight', 'playerWidth', 'tracks', 'nLog'
    ];

    for (const key of keys) {
      const currentValue = current[key];
      const lastValue = this.lastReported[key];

      if (currentValue !== lastValue) {
        delta[key] = currentValue;
        (this.lastReported as Record<string, unknown>)[key] = currentValue;
        hasChanges = true;
      }
    }

    // Include logs if there are new ones
    const lastLogCount = this.lastReported.nLog ?? 0;
    if (this.logs.length > lastLogCount) {
      const newLogs = this.logs.slice(lastLogCount);
      if (newLogs.length > 0) {
        delta.logs = newLogs;
        hasChanges = true;
      }
    }

    if (hasChanges) {
      this.report(delta);
    }

    // Schedule next report
    this.scheduleNextReport();
  }

  /**
   * Start periodic reporting
   */
  private startReporting(): void {
    this.scheduleNextReport();
  }

  /**
   * Schedule the next report
   */
  private scheduleNextReport(): void {
    this.timers.start(() => {
      this.reportStats();
    }, this.reportInterval, 'report');
  }

  /**
   * Send final report and cleanup
   * E2: Flushes immediately since this is a critical event
   */
  sendFinalReport(reason?: string): void {
    // Report final stats
    this.reportStats();

    // Queue unload report
    this.report({ unload: reason ?? null });

    // E2: Flush immediately - don't wait for batch interval
    this.flushBatch();
  }

  /**
   * Stop reporting and cleanup
   */
  destroy(): void {
    // Stop all timers
    this.timers.destroy();

    // Remove event listeners
    this.listeners.forEach(cleanup => cleanup());
    this.listeners = [];

    this.videoElement = null;
    this.containerElement = null;
  }

  /**
   * Add a log entry
   */
  log(message: string): void {
    this.logs.push(message);
  }
}

export default MistReporter;
