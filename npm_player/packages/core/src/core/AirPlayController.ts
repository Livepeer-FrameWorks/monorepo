interface WebKitVideoElement extends HTMLVideoElement {
  webkitShowPlaybackTargetPicker(): void;
}

export class AirPlayController {
  private video: WebKitVideoElement | null = null;
  private available = false;
  private handler: ((e: Event) => void) | null = null;

  isSupported(): boolean {
    return "WebKitPlaybackTargetAvailabilityEvent" in window;
  }

  attach(video: HTMLVideoElement): void {
    this.detach();
    this.video = video as WebKitVideoElement;
    this.handler = (e: Event) => {
      this.available = (e as any).availability === "available";
    };
    this.video.addEventListener("webkitplaybacktargetavailabilitychanged", this.handler);
  }

  isAvailable(): boolean {
    return this.available;
  }

  showPicker(): void {
    this.video?.webkitShowPlaybackTargetPicker();
  }

  detach(): void {
    if (this.video && this.handler) {
      this.video.removeEventListener("webkitplaybacktargetavailabilitychanged", this.handler);
    }
    this.video = null;
    this.handler = null;
    this.available = false;
  }
}
