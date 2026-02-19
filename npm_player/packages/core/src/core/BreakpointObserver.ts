export interface BreakpointConfig {
  breakpoints?: Record<string, number>;
}

const DEFAULT_BREAKPOINTS: Record<string, number> = {
  xs: 0,
  sm: 320,
  md: 480,
  lg: 640,
  xl: 960,
};

export class BreakpointObserver {
  private breakpoints: [string, number][];
  private observer: ResizeObserver | null = null;
  private container: HTMLElement | null = null;
  private currentBreakpoint = "";
  private rafId = 0;

  constructor(config: BreakpointConfig = {}) {
    const bp = config.breakpoints ?? DEFAULT_BREAKPOINTS;
    // Sort descending by value so the first match is the largest qualifying breakpoint.
    this.breakpoints = Object.entries(bp).sort((a, b) => b[1] - a[1]);
  }

  attach(container: HTMLElement): void {
    this.detach();
    this.container = container;

    this.observer = new ResizeObserver(() => {
      if (this.rafId) cancelAnimationFrame(this.rafId);
      this.rafId = requestAnimationFrame(() => {
        this.rafId = 0;
        this.update();
      });
    });

    this.observer.observe(container);
    this.update();
  }

  getCurrentBreakpoint(): string {
    return this.currentBreakpoint;
  }

  detach(): void {
    if (this.rafId) {
      cancelAnimationFrame(this.rafId);
      this.rafId = 0;
    }
    if (this.observer) {
      this.observer.disconnect();
      this.observer = null;
    }
    if (this.container) {
      this.container.removeAttribute("data-size");
      this.container = null;
    }
    this.currentBreakpoint = "";
  }

  private update(): void {
    if (!this.container) return;
    const width = this.container.clientWidth;
    const match = this.breakpoints.find(([, min]) => width >= min);
    const name = match ? match[0] : this.breakpoints[this.breakpoints.length - 1][0];

    if (name !== this.currentBreakpoint) {
      this.currentBreakpoint = name;
      this.container.setAttribute("data-size", name);
    }
  }
}
