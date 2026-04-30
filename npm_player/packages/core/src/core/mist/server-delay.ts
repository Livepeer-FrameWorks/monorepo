export class ServerDelayTracker {
  private delays: number[] = [];
  private pending = new Map<string, number>();

  constructor(
    private readonly maxSamples = 3,
    private readonly now: () => number = () => performance.now()
  ) {}

  beginRequest(requestType: string): void {
    this.pending.set(requestType, this.now());
  }

  resolveRequest(requestType: string): number | null {
    const startTime = this.pending.get(requestType);
    if (startTime === undefined) {
      return null;
    }
    this.pending.delete(requestType);

    const delay = this.now() - startTime;
    this.delays.push(delay);
    if (this.delays.length > this.maxSamples) {
      this.delays.shift();
    }
    return delay;
  }

  getAverageDelay(defaultMs = 0): number {
    if (this.delays.length === 0) {
      return defaultMs;
    }
    return this.delays.reduce((sum, d) => sum + d, 0) / this.delays.length;
  }

  clear(): void {
    this.pending.clear();
    this.delays = [];
  }
}
