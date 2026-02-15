import { BasePlayer } from "../core/PlayerInterface";
import type {
  StreamSource,
  StreamInfo,
  PlayerOptions,
  PlayerCapability,
} from "../core/PlayerInterface";

/**
 * MistPlayerImpl - Legacy fallback player
 *
 * Simple passthrough to MistServer's native player.js embed.
 * No codec preferences or fancy params - just let MistServer handle everything.
 * This is the final fallback when other players fail.
 */
export class MistPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "Legacy",
    shortname: "mist-legacy",
    priority: 99, // Final fallback - lowest priority
    // Single special type - PlayerManager adds this as ONE option
    mimes: ["mist/legacy"],
  };

  private container: HTMLElement | null = null;
  private mistDiv: HTMLDivElement | null = null;
  private proxyVideo: HTMLVideoElement | null = null;
  private previousContainerOverflow: string | null = null;
  private destroyed = false;

  isMimeSupported(mimetype: string): boolean {
    // Only match our special type - PlayerManager handles adding us once
    return mimetype === "mist/legacy";
  }

  isBrowserSupported(
    mimetype: string,
    _source: StreamSource,
    _streamInfo: StreamInfo
  ): boolean | string[] {
    // Only compatible with our special type
    if (mimetype !== "mist/legacy") return false;
    return ["video", "audio"];
  }

  async initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions
  ): Promise<HTMLVideoElement> {
    this.destroyed = false;
    this.container = container;
    container.classList.add("fw-player-container");

    // Generate unique ID for this embed
    const streamName = source.streamName || "stream";
    const uniqueId = `${streamName.replace(/[^a-zA-Z0-9]/g, "_")}_${Math.random().toString(36).slice(2, 10)}`;

    // Create the mistvideo div
    this.mistDiv = document.createElement("div");
    this.mistDiv.className = "mistvideo fw-player-container";
    this.mistDiv.id = uniqueId;
    this.mistDiv.style.width = "100%";
    this.mistDiv.style.height = "100%";
    this.mistDiv.style.overflow = "hidden"; // Prevent legacy player overflow
    // Also on container, but restore on destroy (don't clobber consumer styles permanently)
    this.previousContainerOverflow = container.style.overflow ?? "";
    container.style.overflow = "hidden";
    container.appendChild(this.mistDiv);

    // Derive player.js URL from source URL
    const playerJsUrl = this.getPlayerJsUrl(source);

    // Load and call mistPlay - simple passthrough, no extra params
    await this.loadAndPlay(streamName, this.mistDiv, playerJsUrl, options);

    // Find the video element MistServer created (for compatibility)
    const video = this.findVideoElement() || this.createProxyVideo(container);
    this.videoElement = video;
    this.setupVideoEventListeners(video, options);

    return video;
  }

  private getPlayerJsUrl(source: StreamSource): string {
    // Try to derive player.js URL from source
    if (source.mistPlayerUrl) return source.mistPlayerUrl;

    try {
      const url = new URL(source.url);
      // Use same protocol, different path
      return `${url.protocol}//${url.host}/player.js`;
    } catch {
      // Fallback: relative path
      return "/player.js";
    }
  }

  private async loadAndPlay(
    streamName: string,
    targetElement: HTMLElement,
    playerJsUrl: string,
    options: PlayerOptions
  ): Promise<void> {
    const play = () => {
      if (this.destroyed) return;
      if ((window as any).mistPlay) {
        // Let MistServer handle source selection - no forcePriority
        const mistOptions: any = {
          target: targetElement,
          fillSpace: true,
          // MistServer's player.js has its own UI - always enable controls
          controls: true,
          // Use dev skin when devMode is enabled - shows MistServer's source selection UI
          skin: options.devMode ? "dev" : "default",
          // Only pass basic playback options
          ...(options.autoplay !== undefined && { autoplay: options.autoplay }),
          ...(options.muted !== undefined && { muted: options.muted }),
          ...(options.poster && { poster: options.poster }),
        };

        console.debug("[Legacy] mistPlay options:", mistOptions);
        (window as any).mistPlay(streamName, mistOptions);
      }
    };

    if (!(window as any).mistplayers) {
      // Load player.js
      await new Promise<void>((resolve, reject) => {
        const script = document.createElement("script");
        script.src = playerJsUrl;
        script.onload = () => {
          play();
          resolve();
        };
        script.onerror = () =>
          reject(new Error(`Failed to load MistServer player from ${playerJsUrl}`));
        document.head.appendChild(script);
      });
    } else {
      play();
    }
  }

  private findVideoElement(): HTMLVideoElement | null {
    if (!this.mistDiv) return null;
    return this.mistDiv.querySelector("video");
  }

  private createProxyVideo(container: HTMLElement): HTMLVideoElement {
    const video = document.createElement("video");
    video.style.display = "none";
    container.appendChild(video);
    this.proxyVideo = video;
    return video;
  }

  async destroy(): Promise<void> {
    this.destroyed = true;

    // Try to unload via MistServer API
    if (this.mistDiv) {
      try {
        const ref = (this.mistDiv as any).MistVideoObject?.reference;
        if (ref && typeof ref.unload === "function") {
          ref.unload();
        }
      } catch {
        // Ignore cleanup errors
      }
      try {
        this.mistDiv.remove();
      } catch {
        // Fallback for older DOM implementations
        try {
          this.mistDiv.parentNode?.removeChild(this.mistDiv);
        } catch {}
      }
      this.mistDiv = null;
    }

    if (this.proxyVideo) {
      try {
        this.proxyVideo.remove();
      } catch {
        try {
          this.proxyVideo.parentNode?.removeChild(this.proxyVideo);
        } catch {}
      }
      this.proxyVideo = null;
    }

    if (this.container) {
      // Restore container overflow if we changed it.
      try {
        if (this.previousContainerOverflow !== null) {
          this.container.style.overflow = this.previousContainerOverflow;
        }
      } catch {}
    }

    this.videoElement = null;
    this.container = null;
    this.previousContainerOverflow = null;
    this.listeners.clear();
  }
}
