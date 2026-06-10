import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { BreakpointObserver } from "../src/core/BreakpointObserver";

let resizeCallback: (() => void) | null = null;

class FakeResizeObserver {
  observe = vi.fn();
  disconnect = vi.fn();
  constructor(cb: () => void) {
    resizeCallback = cb;
  }
}

interface FakeEl {
  clientWidth: number;
  setAttribute: ReturnType<typeof vi.fn>;
  removeAttribute: ReturnType<typeof vi.fn>;
  attrs: Record<string, string>;
}

function makeContainer(width: number): FakeEl {
  const attrs: Record<string, string> = {};
  return {
    clientWidth: width,
    attrs,
    setAttribute: vi.fn((k: string, v: string) => {
      attrs[k] = v;
    }),
    removeAttribute: vi.fn((k: string) => {
      delete attrs[k];
    }),
  };
}

beforeEach(() => {
  resizeCallback = null;
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  // Run the rAF coalescing synchronously so update() is observable inline.
  vi.stubGlobal("requestAnimationFrame", (cb: () => void) => {
    cb();
    return 1;
  });
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("BreakpointObserver — width classification", () => {
  it("picks the largest breakpoint whose min the width meets", () => {
    const cases: Array<[number, string]> = [
      [1000, "xl"],
      [700, "lg"],
      [500, "md"],
      [400, "sm"],
      [100, "xs"],
      [0, "xs"],
    ];
    for (const [width, expected] of cases) {
      const obs = new BreakpointObserver();
      const el = makeContainer(width);
      obs.attach(el as unknown as HTMLElement);
      expect(obs.getCurrentBreakpoint()).toBe(expected);
      expect(el.attrs["data-size"]).toBe(expected);
    }
  });

  it("honors a custom breakpoint config", () => {
    const obs = new BreakpointObserver({ breakpoints: { small: 0, big: 800 } });
    const el = makeContainer(900);
    obs.attach(el as unknown as HTMLElement);
    expect(obs.getCurrentBreakpoint()).toBe("big");

    el.clientWidth = 500;
    resizeCallback!();
    expect(obs.getCurrentBreakpoint()).toBe("small");
  });
});

describe("BreakpointObserver — change-only emission", () => {
  it("only writes data-size when the breakpoint actually changes", () => {
    const obs = new BreakpointObserver();
    const el = makeContainer(1000);
    obs.attach(el as unknown as HTMLElement); // initial → xl (1 write)
    expect(el.setAttribute).toHaveBeenCalledTimes(1);

    // Resize within the same band: no new write.
    el.clientWidth = 980;
    resizeCallback!();
    expect(el.setAttribute).toHaveBeenCalledTimes(1);

    // Cross a band boundary: one new write.
    el.clientWidth = 500;
    resizeCallback!();
    expect(el.setAttribute).toHaveBeenCalledTimes(2);
    expect(el.attrs["data-size"]).toBe("md");
  });
});

describe("BreakpointObserver — lifecycle", () => {
  it("detach disconnects, clears data-size and resets state", () => {
    const obs = new BreakpointObserver();
    const el = makeContainer(1000);
    obs.attach(el as unknown as HTMLElement);
    const observer = obs["observer"] as unknown as FakeResizeObserver;

    obs.detach();
    expect(observer.disconnect).toHaveBeenCalledOnce();
    expect(el.removeAttribute).toHaveBeenCalledWith("data-size");
    expect(obs.getCurrentBreakpoint()).toBe("");
  });

  it("re-attaching detaches the previous container first", () => {
    const obs = new BreakpointObserver();
    const first = makeContainer(1000);
    obs.attach(first as unknown as HTMLElement);
    const second = makeContainer(400);
    obs.attach(second as unknown as HTMLElement);
    // The first container was cleaned up by the implicit detach() in attach().
    expect(first.removeAttribute).toHaveBeenCalledWith("data-size");
    expect(obs.getCurrentBreakpoint()).toBe("sm");
  });
});
