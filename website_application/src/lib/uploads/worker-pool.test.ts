import { describe, expect, it } from "vitest";
import { WorkerPool } from "./worker-pool";

const tick = () => new Promise<void>((r) => setTimeout(r, 0));

describe("WorkerPool", () => {
  it("respects the concurrency cap", async () => {
    let inFlight = 0;
    let peak = 0;
    const pool = new WorkerPool<number, void>({
      concurrency: 3,
      handler: async () => {
        inFlight++;
        peak = Math.max(peak, inFlight);
        await tick();
        await tick();
        inFlight--;
      },
    });
    pool.enqueue(Array.from({ length: 10 }, (_, i) => ({ id: `${i}`, payload: i })));
    pool.start();
    await pool.drain();
    expect(peak).toBe(3);
  });

  it("processes all items by default", async () => {
    const seen: number[] = [];
    const pool = new WorkerPool<number, void>({
      concurrency: 2,
      handler: async (item) => {
        seen.push(item.payload);
      },
    });
    pool.enqueue([1, 2, 3, 4].map((n) => ({ id: `${n}`, payload: n })));
    pool.start();
    await pool.drain();
    expect(seen.sort()).toEqual([1, 2, 3, 4]);
  });

  it("pause prevents new work; resume picks up the rest", async () => {
    const seen: number[] = [];
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const pool = new WorkerPool<number, void>({
      concurrency: 1,
      handler: async (item) => {
        if (item.payload === 1) await gate;
        seen.push(item.payload);
      },
    });
    pool.enqueue([1, 2, 3].map((n) => ({ id: `${n}`, payload: n })));
    pool.start();
    pool.pause();
    release();
    await tick();
    await tick();
    expect(seen).toEqual([1]);
    pool.resume();
    await pool.drain();
    expect(seen.sort()).toEqual([1, 2, 3]);
  });

  it("abort drops the queue and aborts in-flight", async () => {
    const aborted: number[] = [];
    const pool = new WorkerPool<number, void>({
      concurrency: 2,
      handler: async (item, signal) => {
        await new Promise<void>((resolve, reject) => {
          signal.addEventListener("abort", () => {
            aborted.push(item.payload);
            reject(new Error("aborted"));
          });
          setTimeout(resolve, 1000);
        });
      },
    });
    pool.enqueue([1, 2, 3, 4].map((n) => ({ id: `${n}`, payload: n })));
    pool.start();
    await tick();
    pool.abort();
    await pool.drain();
    expect(aborted.sort()).toEqual([1, 2]);
    expect(pool.pendingCount()).toBe(0);
  });

  it("abort resolves drain after pause leaves queued work", async () => {
    const seen: number[] = [];
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const pool = new WorkerPool<number, void>({
      concurrency: 1,
      handler: async (item) => {
        if (item.payload === 1) await gate;
        seen.push(item.payload);
      },
    });
    pool.enqueue([1, 2, 3].map((n) => ({ id: `${n}`, payload: n })));
    pool.start();
    pool.pause();
    const drained = pool.drain().then(() => "drained");
    release();
    await tick();
    await tick();
    expect(seen).toEqual([1]);
    pool.abort();
    await expect(drained).resolves.toBe("drained");
    expect(pool.pendingCount()).toBe(0);
  });
});
