export interface WorkItem<T> {
  id: string;
  payload: T;
}

export type WorkHandler<T, R> = (item: WorkItem<T>, signal: AbortSignal) => Promise<R>;

export interface WorkerPoolEvents<T, R> {
  onItemDone?: (item: WorkItem<T>, result: R) => void;
  onItemError?: (item: WorkItem<T>, err: unknown) => void;
}

export interface WorkerPoolOptions<T, R> {
  concurrency: number;
  handler: WorkHandler<T, R>;
  events?: WorkerPoolEvents<T, R>;
}

export class WorkerPool<T, R> {
  private queue: WorkItem<T>[] = [];
  private inFlight = new Set<WorkItem<T>>();
  private aborts = new Map<WorkItem<T>, AbortController>();
  private running = false;
  private paused = false;
  private drainResolvers: Array<() => void> = [];

  constructor(private readonly opts: WorkerPoolOptions<T, R>) {
    if (opts.concurrency < 1) throw new Error("concurrency must be >= 1");
  }

  enqueue(items: WorkItem<T>[]): void {
    this.queue.push(...items);
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    this.paused = false;
    this.fill();
  }

  pause(): void {
    this.paused = true;
  }

  resume(): void {
    if (!this.running) return;
    this.paused = false;
    this.fill();
  }

  abort(): void {
    this.running = false;
    this.paused = false;
    this.queue = [];
    for (const ac of this.aborts.values()) ac.abort();
    this.resolveDrainsIfIdle();
  }

  inFlightCount(): number {
    return this.inFlight.size;
  }

  pendingCount(): number {
    return this.queue.length;
  }

  drain(): Promise<void> {
    if (this.inFlight.size === 0 && this.queue.length === 0) {
      return Promise.resolve();
    }
    return new Promise((resolve) => this.drainResolvers.push(resolve));
  }

  private fill(): void {
    if (!this.running || this.paused) return;
    while (this.inFlight.size < this.opts.concurrency && this.queue.length > 0) {
      const item = this.queue.shift()!;
      this.run(item);
    }
  }

  private async run(item: WorkItem<T>): Promise<void> {
    const ac = new AbortController();
    this.inFlight.add(item);
    this.aborts.set(item, ac);
    try {
      const result = await this.opts.handler(item, ac.signal);
      this.opts.events?.onItemDone?.(item, result);
    } catch (err) {
      this.opts.events?.onItemError?.(item, err);
    } finally {
      this.inFlight.delete(item);
      this.aborts.delete(item);
      if (this.running && !this.paused) {
        this.fill();
      }
      if (this.inFlight.size === 0 && this.queue.length === 0) {
        this.resolveDrainsIfIdle();
      }
    }
  }

  private resolveDrainsIfIdle(): void {
    if (this.inFlight.size !== 0 || this.queue.length !== 0) return;
    const resolvers = this.drainResolvers;
    this.drainResolvers = [];
    for (const r of resolvers) r();
  }
}
