/**
 * registerPlayer() — Simplified API for registering custom protocol players.
 *
 * Bridges a simple definition object to the full IPlayer interface and registers
 * it with the global player manager.
 *
 * @example
 * ```ts
 * import { registerPlayer } from '@livepeer-frameworks/player-core';
 *
 * registerPlayer('myproto', {
 *   name: 'My Protocol Player',
 *   priority: 5,
 *   mimeTypes: ['application/x-myproto'],
 *   isBrowserSupported: () => typeof RTCPeerConnection !== 'undefined',
 *   async build(source, video, container) {
 *     // Set up playback on the <video> element
 *     video.src = source.url;
 *   },
 *   destroy() {
 *     // Clean up resources
 *   },
 * });
 * ```
 */

import {
  BasePlayer,
  type PlayerCapability,
  type StreamSource,
  type StreamInfo,
  type PlayerOptions,
} from "../core/PlayerInterface";
import { globalPlayerManager, ensurePlayersRegistered } from "../core/PlayerRegistry";

export interface SimplePlayerDefinition {
  /** Display name */
  name: string;
  /** Priority (lower = higher priority, default: 10) */
  priority?: number;
  /** MIME types this player handles */
  mimeTypes: string[];
  /** Browser support check (default: () => true) */
  isBrowserSupported?: () => boolean;
  /** Build/initialize the player. Receives source info and a pre-created <video> element. */
  build(
    source: StreamSource,
    video: HTMLVideoElement,
    container: HTMLElement
  ): void | Promise<void>;
  /** Clean up resources */
  destroy?(): void;
}

class SimplePlayerAdapter extends BasePlayer {
  readonly capability: PlayerCapability;
  private _def: SimplePlayerDefinition;
  private _video: HTMLVideoElement | null = null;

  constructor(shortname: string, def: SimplePlayerDefinition) {
    super();
    this.capability = {
      name: def.name,
      shortname,
      priority: def.priority ?? 10,
      mimes: def.mimeTypes,
    };
    this._def = def;
  }

  isMimeSupported(mimetype: string): boolean {
    return this._def.mimeTypes.includes(mimetype);
  }

  isBrowserSupported(
    _mimetype: string,
    _source: StreamSource,
    _streamInfo: StreamInfo
  ): boolean | string[] {
    return this._def.isBrowserSupported ? this._def.isBrowserSupported() : true;
  }

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    _options: PlayerOptions,
    _streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement> {
    const video = document.createElement("video");
    video.style.width = "100%";
    video.style.height = "100%";
    video.style.objectFit = "contain";
    container.appendChild(video);
    this._video = video;

    await this._def.build(source, video, container);
    return video;
  }

  async destroy(): Promise<void> {
    this._def.destroy?.();
    if (this._video) {
      this._video.pause();
      this._video.removeAttribute("src");
      this._video.load();
      this._video.remove();
      this._video = null;
    }
  }
}

/**
 * Register a custom protocol player with the global player manager.
 *
 * @param shortname - Unique identifier for this player (e.g. 'myproto')
 * @param definition - Player definition
 */
export function registerPlayer(shortname: string, definition: SimplePlayerDefinition): void {
  ensurePlayersRegistered();
  const adapter = new SimplePlayerAdapter(shortname, definition);
  globalPlayerManager.registerPlayer(adapter);
}
