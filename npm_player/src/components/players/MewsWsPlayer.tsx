import React, { useEffect, useRef } from 'react';
import { BasePlayer, StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../core/PlayerInterface';

type Props = {
  wsUrl: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

export class MewsWsPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "MEWS WebSocket Player",
    shortname: "mews",
    priority: 6,
    mimes: ["ws/video/mp4", "ws/video/webm"]
  };

  private ws: WebSocket | null = null;
  private ms: MediaSource | null = null;
  private sb: SourceBuffer | null = null;
  private sbQueue: Uint8Array[] = [];
  private sbBusy = false;
  private sbDoOnUpdateEnd: Array<() => void> = [];
  private objectUrl: string | null = null;
  private container: HTMLElement | null = null;
  private lastDuration = Infinity;
  private pausedByHold = false;
  private serverDelaySamples: number[] = [];
  private pendingDelayTypes: Record<string, number> = {};
  private isDestroyed = false;
  private reconnectAttempts = 0;
  private maxReconnectAttempts = 5;
  private reconnectTimer: any = null;
  private wasConnected = false;
  private pendingSeekMs: number | null = null;
  private pendingSwitchMs: number | null = null;
  private bitCounter: number[] = [];
  private bitsSince: number[] = [];
  private currentBps: number | null = null;
  private waitingCount = 0;
  private abrEnabled = true;
  private analyticsEnabled = false;
  private analyticsEndpoint: string | null = null;
  private analyticsTimer: any = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
    try {
      const extras: any = source as any;
      if (extras && extras.disableOnMacOS) {
        const isMac = navigator.platform.toUpperCase().indexOf('MAC') >= 0;
        if (isMac) return false;
      }
    } catch {}
    if (!window.MediaSource || !window.WebSocket) return false;
    const mp4Ok = MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E, mp4a.40.2"');
    const webmOk = MediaSource.isTypeSupported('video/webm; codecs="vp9, opus"') || MediaSource.isTypeSupported('video/webm; codecs="vp8, vorbis"');
    if (!mp4Ok && !webmOk) return false;
    return ['video', 'audio'];
  }

  async initialize(container: HTMLElement, source: StreamSource, options: PlayerOptions): Promise<HTMLVideoElement> {
    this.container = container;
    
    const video = document.createElement('video');
    video.style.width = '100%';
    video.style.height = '100%';
    video.setAttribute('playsinline', '');
    video.setAttribute('crossorigin', 'anonymous');
    
    if (options.autoplay) video.autoplay = true;
    if (options.muted) video.muted = true;
    if (options.controls) video.controls = true;
    if (options.loop) video.loop = true;
    if (options.poster) video.poster = options.poster;

    this.videoElement = video;
    container.appendChild(video);

    this.setupVideoEventListeners(video, options);

    try {
      // Analytics configuration (optional; read via any-cast to avoid tight coupling)
      const anyOpts: any = options as any;
      this.analyticsEnabled = !!anyOpts.analytics?.enabled;
      this.analyticsEndpoint = anyOpts.analytics?.endpoint || null;

      this.ms = new MediaSource();
      const onSourceOpen = () => this.handleSourceOpen(source);
      this.ms.addEventListener('sourceopen', onSourceOpen);
      this.objectUrl = URL.createObjectURL(this.ms);
      video.src = this.objectUrl;
      this.isDestroyed = false;
      this.startTelemetry();
      return video;
      
    } catch (error: any) {
      this.emit('error', error.message || String(error));
      throw error;
    }
  }

  destroy(): void {
    this.isDestroyed = true;
    if (this.analyticsTimer) {
      try { clearInterval(this.analyticsTimer); } catch {}
      this.analyticsTimer = null;
    }
    if (this.reconnectTimer) {
      try { clearTimeout(this.reconnectTimer); } catch {}
      this.reconnectTimer = null;
    }
    try { this.ws?.close(); } catch {}
    this.ws = null;
    if (this.sb) {
      try { this.sb.abort(); } catch {}
    }
    this.sb = null;
    if (this.ms) {
      try { if (this.ms.readyState === 'open') this.ms.endOfStream(); } catch {}
    }
    if (this.objectUrl) {
      try { URL.revokeObjectURL(this.objectUrl); } catch {}
    }
    if (this.videoElement && this.container) {
      this.container.removeChild(this.videoElement);
    }
    this.videoElement = null;
    this.container = null;
    this.ms = null;
    this.objectUrl = null;
    this.sbQueue = [];
    this.sbBusy = false;
    this.sbDoOnUpdateEnd = [];
    this.listeners.clear();
  }

  async play(): Promise<void> {
    if (!this.ws) return;
    this.send({ type: 'play' });
    await this.videoElement?.play();
  }

  pause(): void {
    if (!this.ws) return;
    this.videoElement?.pause();
    this.send({ type: 'hold' });
    this.pausedByHold = true;
  }

  seek(time: number): void {
    if (!this.ws || !this.videoElement) return;
    const seekMs = Math.max(0, Math.round(time * 1000 - (250 + this.getServerDelay())));
    this.pendingSeekMs = seekMs;
    this.send({ type: 'seek', seek_time: seekMs });
  }

  private handleSourceOpen(source: StreamSource) {
    if (!this.ms || !this.videoElement) return;
    try {
      this.openWebSocket(source.url);
    } catch (e: any) {
      this.emit('error', e?.message || 'Failed to initialize MEWS');
    }
  }

  private openWebSocket(url: string) {
    // Protocol mismatch check (non-fatal)
    try {
      const pageProto = window.location.protocol.replace(/^http/, 'ws');
      const srcProto = new URL(url, window.location.href).protocol;
      if (pageProto !== srcProto) {
        this.emit('error', `Protocol mismatch ${pageProto} vs ${srcProto}`);
      }
    } catch {}
    const ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    this.ws = ws;

    ws.onopen = () => {
      this.wasConnected = true;
      this.reconnectAttempts = 0;
      if (this.reconnectTimer) { try { clearTimeout(this.reconnectTimer); } catch {} this.reconnectTimer = null; }
      this.requestCodecData();
    };

    ws.onmessage = (e: MessageEvent<ArrayBuffer | string>) => {
      if (typeof e.data === 'string') {
        try {
          const msg = JSON.parse(e.data as string);
          this.handleControlMessage(msg);
        } catch {}
        return;
      }
      const data = new Uint8Array(e.data as ArrayBuffer);
      if (!this.sb) {
        this.queueOrBuffer(data);
        return;
      }
      this.appendToBuffer(data);
      // bitrate monitoring
      this.trackBits(e.data as ArrayBuffer);
    };

    ws.onerror = () => this.emit('error', 'WebSocket error');
    ws.onclose = () => {
      if (this.isDestroyed) return;
      if (this.wasConnected && this.reconnectAttempts < this.maxReconnectAttempts) {
        const backoff = Math.min(5000, 500 * Math.pow(2, this.reconnectAttempts));
        this.reconnectAttempts++;
        this.reconnectTimer = setTimeout(() => {
          if (!this.isDestroyed && this.videoElement) {
            this.openWebSocket(url);
          }
        }, backoff);
      } else {
        this.emit('error', 'WebSocket closed');
      }
    };
  }

  private queueOrBuffer(data: Uint8Array) {
    if (!data || !data.byteLength) return;
    this.sbQueue.push(data);
  }

  private handleControlMessage(msg: any) {
    switch (msg.type) {
      case 'codec_data': {
        const codecs: string[] = msg?.data?.codecs || [];
        if (!this.ms) return;
        if (!codecs.length) {
          this.emit('error', 'No codecs provided');
          return;
        }
        const mime = `video/mp4; codecs="${codecs.join(',')}"`;
        if (!MediaSource.isTypeSupported(mime)) {
          this.emit('error', `Unsupported MSE codec: ${mime}`);
          return;
        }
        if (!this.sb) {
          this.sb = this.ms.addSourceBuffer(mime);
          this.installSourceBufferHandlers();
          if (this.sbQueue.length) {
            const pending = this.sbQueue.slice();
            this.sbQueue = [];
            for (const frag of pending) this.appendToBuffer(frag);
          }
        }
        this.resolveDelay('codec_data');
        break;
      }
      case 'on_time': {
        const end = msg?.data?.end;
        if (typeof end === 'number') {
          this.lastDuration = end * 1e-3;
        }
        // live catch-up tuning
        if (this.videoElement && typeof msg?.data?.current === 'number') {
          const v = this.videoElement;
          const buffer = msg.data.current - v.currentTime * 1e3;
          const desired = 2 * this.getServerDelay();
          this.tunePlaybackRate(buffer, desired);
        }
        // Seek acknowledgment handling
        const currentMs = msg?.data?.current;
        if (this.pendingSeekMs !== null && typeof currentMs === 'number' && currentMs >= this.pendingSeekMs) {
          const tSec = (currentMs * 1e-3).toFixed(3);
          if (this.videoElement) {
            const v = this.videoElement;
            const pos = parseFloat(tSec);
            const i = this.findBufferIndex(pos);
            if (i !== false) {
              v.currentTime = pos;
              this.pendingSeekMs = null;
            }
          }
        }
        break;
      }
      case 'on_stop': {
        if (this.ms && this.ms.readyState === 'open') {
          try { this.ms.endOfStream(); } catch {}
        }
        break;
      }
      case 'tracks': {
        const newCodecs: string[] = msg?.data?.codecs || [];
        const switchingPointMs: number | undefined = msg?.data?.current;
        if (!newCodecs.length || !this.ms) return;
        const mime = `video/mp4; codecs="${newCodecs.join(',')}"`;
        if (!MediaSource.isTypeSupported(mime)) return;
        if (typeof switchingPointMs === 'number') {
          this.pendingSwitchMs = switchingPointMs;
          this.awaitSwitchingPoint(mime, switchingPointMs);
        } else {
          const remaining = this.sbQueue;
          this.sbQueue = [];
          if (this.sb) {
            try {
              if (this.ms.readyState === 'open') {
                this.ms.removeSourceBuffer(this.sb);
              }
            } catch {}
          }
          this.sb = this.ms.addSourceBuffer(mime);
          this.installSourceBufferHandlers();
          for (const frag of remaining) this.appendToBuffer(frag);
        }
        break;
      }
      case 'seek':
      case 'set_speed': {
        this.resolveDelay(msg.type);
        break;
      }
    }
  }

  private installSourceBufferHandlers() {
    if (!this.sb || !this.videoElement) return;
    this.sb.mode = 'segments';
    this.sb.addEventListener('updateend', () => {
      this.sbBusy = false;
      if (!this.sb || this.sb.updating) return;
      const next = this.sbQueue.shift();
      if (next) this._append(next);
      // drain scheduled ops
      const toRun = this.sbDoOnUpdateEnd.slice();
      this.sbDoOnUpdateEnd = [];
      for (const fn of toRun) {
        try { fn(); } catch {}
      }
    });

    // error guard
    this.sb.addEventListener('error', () => {
      this.emit('error', 'SourceBuffer error');
    });

    const v = this.videoElement;
    v.addEventListener('waiting', () => {
      if (!v.buffered || v.buffered.length === 0) return;
      const i = this.findBufferIndex(v.currentTime);
      if (i !== false && i + 1 < v.buffered.length) {
        const nextStart = v.buffered.start(i + 1);
        if (nextStart - v.currentTime < 10) {
          v.currentTime = nextStart;
        }
      }
      // ABR trigger on repeated waiting
      if (this.abrEnabled) {
        this.waitingCount++;
        if (this.waitingCount >= 3) {
          this.waitingCount = 0;
          if (this.currentBps) this.requestLowerBitrate(this.currentBps);
        }
      }
    });
  }

  private appendToBuffer(data: Uint8Array) {
    if (!this.sb || !data || !data.byteLength) return;
    if (this.sb.updating || this.sbBusy) {
      this.sbQueue.push(data);
      return;
    }
    this._append(data);
  }

  private _append(data: Uint8Array) {
    if (!this.sb) return;
    try {
      this.sbBusy = true;
      // Handle both ArrayBuffer and SharedArrayBuffer without copying
      if (data.buffer instanceof SharedArrayBuffer) {
        // Only copy if it's SharedArrayBuffer (rare case)
        const buffer = new ArrayBuffer(data.byteLength);
        new Uint8Array(buffer).set(data);
        this.sb.appendBuffer(buffer);
      } else {
        // ArrayBuffer path - just slice to get the right range
        this.sb.appendBuffer(data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength));
      }
    } catch (e: any) {
      if (e?.name === 'QuotaExceededError') {
        this.cleanBuffer(10);
        this.sbBusy = false;
        this.sbQueue.unshift(data);
        return;
      }
      this.emit('error', e?.message || 'Append buffer failed');
      this.sbBusy = false;
    }
  }

  private cleanBuffer(keepAwaySeconds: number) {
    if (!this.sb || !this.videoElement) return;
    const v = this.videoElement;
    if (!v.buffered || v.buffered.length === 0) return;
    const end = v.currentTime - Math.max(0.1, keepAwaySeconds);
    if (end <= 0) return;
    try {
      this.sb.remove(0, end);
    } catch {}
  }

  private requestCodecData() {
    const supported: string[] = [];
    if (MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E"')) supported.push('avc1.42E01E');
    if (MediaSource.isTypeSupported('audio/mp4; codecs="mp4a.40.2"')) supported.push('mp4a.40.2');
    this.logDelay('codec_data');
    this.send({ type: 'request_codec_data', supported_codecs: supported });
  }

  private send(cmd: any) {
    if (!this.ws) return;
    if (this.ws.readyState === this.ws.OPEN) {
      try {
        this.ws.send(JSON.stringify(cmd));
      } catch {}
    }
  }

  private logDelay(type: string) {
    this.pendingDelayTypes[type] = Date.now();
  }
  private resolveDelay(type: string) {
    const start = this.pendingDelayTypes[type];
    if (start) {
      const dt = Date.now() - start;
      this.serverDelaySamples.unshift(dt);
      if (this.serverDelaySamples.length > 5) this.serverDelaySamples.pop();
      delete this.pendingDelayTypes[type];
    }
  }
  private getServerDelay(): number {
    if (!this.serverDelaySamples.length) return 500;
    const n = Math.min(3, this.serverDelaySamples.length);
    const sum = this.serverDelaySamples.slice(0, n).reduce((a, b) => a + b, 0);
    return Math.round(sum / n);
  }

  private findBufferIndex(position: number): number | false {
    const v = this.videoElement!;
    for (let i = 0; i < v.buffered.length; i++) {
      if (v.buffered.start(i) <= position && v.buffered.end(i) >= position) return i;
    }
    return false;
  }

  // Schedule a function after current update completes
  private sbDo(fn: () => void) {
    if (!this.sb || this.sb.updating || this.sbBusy) {
      this.sbDoOnUpdateEnd.push(fn);
    } else {
      fn();
    }
  }

  private awaitSwitchingPoint(newMime: string, switchPointMs: number) {
    const v = this.videoElement!;
    const tSec = switchPointMs * 1e-3;
    const clearAndReinit = () => {
      if (!this.ms) return;
      // Remove old SB and create new with newMime
      this.sbDo(() => {
        try {
          if (this.sb && this.ms!.readyState === 'open') {
            this.ms!.removeSourceBuffer(this.sb);
          }
        } catch {}
        this.sb = this.ms!.addSourceBuffer(newMime);
        this.installSourceBufferHandlers();
        this.pendingSwitchMs = null;
      });
    };

    const onTimeUpdate = () => {
      if (v.currentTime >= tSec) {
        v.removeEventListener('timeupdate', onTimeUpdate);
        v.removeEventListener('waiting', onWaiting);
        clearAndReinit();
      }
    };
    const onWaiting = () => {
      // End of buffer reached; switch now
      v.removeEventListener('timeupdate', onTimeUpdate);
      v.removeEventListener('waiting', onWaiting);
      clearAndReinit();
    };
    v.addEventListener('timeupdate', onTimeUpdate);
    v.addEventListener('waiting', onWaiting);
  }

  private trackBits(buf: ArrayBuffer) {
    // Stats every 500ms window
    this.bitCounter.push(buf.byteLength * 8);
    this.bitsSince.push(Date.now());
    if (this.bitCounter.length > 10) {
      this.bitCounter.shift();
      this.bitsSince.shift();
    }
    const len = this.bitCounter.length;
    if (len >= 2) {
      const bits = this.bitCounter[len - 1] - this.bitCounter[0];
      const dt = (this.bitsSince[len - 1] - this.bitsSince[0]) / 1000;
      if (dt > 0) this.currentBps = Math.max(1, Math.round(bits / dt));
    }
  }

  private requestLowerBitrate(currentBps: number) {
    // Ask server for lower bitrate track; mimic MistMeta string format
    const hint = `<${currentBps}bps,minbps`;
    this.send({ type: 'tracks', video: hint });
  }

  // Override duration/currentTime accessors for live edge awareness
  getCurrentTime(): number {
    return this.videoElement?.currentTime ?? 0;
  }
  getDuration(): number {
    return isFinite(this.lastDuration) ? this.lastDuration : super.getDuration();
  }

  // Live catch-up: exposed via on_time and buffer distance
  private tunePlaybackRate(bufferMs: number, desiredBufferMs: number) {
    const v = this.videoElement;
    if (!v) return;
    if (bufferMs > desiredBufferMs * 2) {
      v.playbackRate = Math.min(2, v.playbackRate * 1.05);
    } else if (bufferMs < desiredBufferMs / 2) {
      v.playbackRate = Math.max(0.8, v.playbackRate * 0.95);
    } else if (Math.abs(v.playbackRate - 1) > 0.01) {
      v.playbackRate = 1;
    }
  }

  setPlaybackRate(rate: number): void {
    super.setPlaybackRate(rate);
    // Map to server set_speed; 1 => auto
    const play_rate = rate === 1 ? 'auto' : rate;
    this.send({ type: 'set_speed', play_rate });
  }

  // Optional qualities: expose Auto only for now (server-driven ABR)
  getQualities(): Array<{ id: string; label: string; bitrate?: number; width?: number; height?: number; isAuto?: boolean; active?: boolean }> {
    return [{ id: 'auto', label: 'Auto', isAuto: true, active: true }];
  }

  selectQuality(id: string): void {
    // MEWS uses server-driven ABR. 'auto' restores default mode.
    if (id === 'auto') this.send({ type: 'set_speed', play_rate: 'auto' });
  }

  async getStats(): Promise<any> {
    return {
      currentBps: this.currentBps,
      waitingEvents: this.waitingCount
    };
  }

  private startTelemetry() {
    if (!this.analyticsEnabled || !this.analyticsEndpoint) return;
    const post = async (payload: any) => {
      try {
        await fetch(this.analyticsEndpoint!, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
      } catch {}
    };
    this.analyticsTimer = setInterval(async () => {
      if (!this.videoElement) return;
      const stats = await this.getStats();
      const payload = {
        t: Date.now(),
        bps: stats.currentBps || 0,
        waiting: stats.waitingEvents || 0
      };
      post(payload);
    }, 5000);
  }
}

const MewsWsPlayer: React.FC<Props> = ({ wsUrl, muted = true, autoPlay = true, controls = true, onError }) => {
  const videoRef = useRef<HTMLVideoElement>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const msRef = useRef<MediaSource | null>(null);
  const sbRef = useRef<SourceBuffer | null>(null);

  useEffect(() => {
    const el = videoRef.current;
    if (!el) return;
    el.muted = muted;
    const ms = new MediaSource();
    msRef.current = ms;
    const onSourceOpen = () => {};
    ms.addEventListener('sourceopen', onSourceOpen);
    const url = URL.createObjectURL(ms);
    el.src = url;
    return () => {
      try { wsRef.current?.close(); } catch {}
      try { if (sbRef.current) sbRef.current.abort(); } catch {}
      try { URL.revokeObjectURL(url); } catch {}
    };
  }, [wsUrl, muted, onError]);

  return (
    <video ref={videoRef} autoPlay={autoPlay} muted={muted} controls={controls} playsInline style={{ width: '100%', height: '100%' }} />
  );
};

export default MewsWsPlayer;


