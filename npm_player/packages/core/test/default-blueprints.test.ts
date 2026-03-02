import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BlueprintContext, BlueprintMap } from "../src/vanilla/Blueprint";

// Set up DOM stubs before importing the module
let origDocument: any;
let createdElements: any[];

function makeMockElement(tag = "div") {
  const eventListeners = new Map<string, Function[]>();
  const children: any[] = [];
  const attrs = new Map<string, string>();
  const classSet = new Set<string>();
  const styleProps = new Map<string, string>();

  const el: any = {
    tagName: tag.toUpperCase(),
    className: "",
    textContent: "",
    innerHTML: "",
    type: "",
    min: "",
    max: "",
    step: "",
    value: "",
    style: new Proxy(styleProps, {
      get(target, prop: string) {
        if (prop === "setProperty") {
          return (k: string, v: string) => target.set(k, v);
        }
        if (prop === "_map") return target;
        return target.get(prop) ?? "";
      },
      set(target, prop: string, value: string) {
        target.set(prop, value);
        return true;
      },
    }),
    classList: {
      add: vi.fn((cls: string) => classSet.add(cls)),
      remove: vi.fn((cls: string) => classSet.delete(cls)),
      contains: vi.fn((cls: string) => classSet.has(cls)),
      _set: classSet,
    },
    setAttribute: vi.fn((key: string, val: string) => attrs.set(key, val)),
    getAttribute: vi.fn((key: string) => attrs.get(key) ?? null),
    appendChild: vi.fn((child: any) => children.push(child)),
    prepend: vi.fn((child: any) => children.unshift(child)),
    addEventListener: vi.fn((evt: string, fn: Function) => {
      if (!eventListeners.has(evt)) eventListeners.set(evt, []);
      eventListeners.get(evt)!.push(fn);
    }),
    removeEventListener: vi.fn(),
    querySelector: vi.fn((_sel: string) => null),
    getBoundingClientRect: vi.fn(() => ({ left: 0, width: 200, top: 0, height: 100 })),
    children,
    _attrs: attrs,
    _classSet: classSet,
    _eventListeners: eventListeners,
    _fireEvent: (evt: string, eventObj?: any) => {
      const listeners = eventListeners.get(evt);
      if (listeners) {
        for (const fn of listeners) fn(eventObj ?? {});
      }
    },
  };

  return el;
}

beforeEach(() => {
  origDocument = (globalThis as any).document;
  createdElements = [];

  (globalThis as any).document = {
    createElement: vi.fn((tag: string) => {
      const el = makeMockElement(tag);
      createdElements.push(el);
      return el;
    }),
  };
});

afterEach(() => {
  (globalThis as any).document = origDocument;
  vi.restoreAllMocks();
});

// Dynamic import so it picks up our document mock
async function getBlueprints(): Promise<{ DEFAULT_BLUEPRINTS: BlueprintMap }> {
  vi.resetModules();
  return import("../src/vanilla/defaultBlueprints");
}

function makeCtx(overrides: Partial<BlueprintContext> = {}): BlueprintContext {
  const subscriptions = new Map<string, ((val: unknown) => void)[]>();

  return {
    video: null,
    subscribe: {
      on: vi.fn((prop: string, cb: (val: unknown) => void) => {
        if (!subscriptions.has(prop)) subscriptions.set(prop, []);
        subscriptions.get(prop)!.push(cb);
        return () => {};
      }),
      get: vi.fn(),
      off: vi.fn(),
    },
    api: {
      togglePlay: vi.fn(),
      toggleMute: vi.fn(),
      toggleFullscreen: vi.fn().mockResolvedValue(undefined),
      togglePiP: vi.fn().mockResolvedValue(undefined),
      skipBack: vi.fn(),
      skipForward: vi.fn(),
      jumpToLive: vi.fn(),
      clearError: vi.fn(),
      retry: vi.fn().mockResolvedValue(undefined),
      seek: vi.fn(),
      currentTime: 0,
      duration: 0,
      live: false,
      volume: 0.5,
    } as any,
    fullscreen: {
      supported: true,
      active: false,
      toggle: vi.fn(),
      request: vi.fn(),
      exit: vi.fn(),
    },
    pip: { supported: true, active: false, toggle: vi.fn() },
    info: null,
    options: {} as any,
    container: makeMockElement() as any,
    translate: vi.fn((key: string, fallback?: string) => fallback ?? key),
    buildIcon: vi.fn(() => null),
    log: vi.fn(),
    timers: {
      setTimeout: vi.fn(),
      clearTimeout: vi.fn(),
      setInterval: vi.fn(),
      clearInterval: vi.fn(),
    },
    // Helper for tests to fire subscriptions
    _subscriptions: subscriptions,
    ...overrides,
  } as any;
}

function fireSub(ctx: any, prop: string, value: unknown) {
  const subs = ctx._subscriptions?.get(prop);
  if (subs) {
    for (const cb of subs) cb(value);
  }
}

describe("defaultBlueprints", () => {
  it("exports all expected blueprint keys", async () => {
    const { DEFAULT_BLUEPRINTS } = await getBlueprints();
    const expectedKeys = [
      "container",
      "videocontainer",
      "controls",
      "controlbar",
      "play",
      "seekBackward",
      "seekForward",
      "live",
      "currentTime",
      "totalTime",
      "speaker",
      "volume",
      "fullscreen",
      "pip",
      "settings",
      "progress",
      "loading",
      "error",
      "spacer",
    ];
    for (const key of expectedKeys) {
      expect(DEFAULT_BLUEPRINTS[key]).toBeDefined();
      expect(typeof DEFAULT_BLUEPRINTS[key]).toBe("function");
    }
  });

  describe("container", () => {
    it("creates root element with correct attributes", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.container(ctx);

      expect(el).not.toBeNull();
      expect(el!.className).toContain("fw-player-surface");
      expect(el!.className).toContain("fw-bp-container");
      expect(el!.setAttribute).toHaveBeenCalledWith("role", "region");
      expect(el!.setAttribute).toHaveBeenCalledWith("tabindex", "0");
    });
  });

  describe("videocontainer", () => {
    it("creates positioned wrapper", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.videocontainer(ctx);

      expect(el).not.toBeNull();
      expect(el!.className).toContain("fw-bp-video-container");
    });
  });

  describe("controls", () => {
    it("creates control bar with gradient", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.controls(ctx);

      expect(el).not.toBeNull();
      expect(el!.className).toContain("fw-bp-controls");
      expect(ctx.subscribe.on).toHaveBeenCalledWith("playing", expect.any(Function));
    });
  });

  describe("controlbar", () => {
    it("creates flex container", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.controlbar(ctx);

      expect(el).not.toBeNull();
      expect(el!.className).toContain("fw-bp-controlbar");
    });
  });

  describe("play", () => {
    it("creates play button that toggles on click", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.play(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-play");
      expect(el.addEventListener).toHaveBeenCalledWith("click", expect.any(Function));

      // Click should toggle play
      el._fireEvent("click");
      expect(ctx.api.togglePlay).toHaveBeenCalled();
    });

    it("updates icons on playing subscription", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      DEFAULT_BLUEPRINTS.play(ctx);

      expect(ctx.subscribe.on).toHaveBeenCalledWith("playing", expect.any(Function));
    });
  });

  describe("seekBackward", () => {
    it("creates button that calls skipBack", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.seekBackward(ctx)!;

      el._fireEvent("click");
      expect(ctx.api.skipBack).toHaveBeenCalledWith(10000);
    });
  });

  describe("seekForward", () => {
    it("creates button that calls skipForward", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.seekForward(ctx)!;

      el._fireEvent("click");
      expect(ctx.api.skipForward).toHaveBeenCalledWith(10000);
    });
  });

  describe("live", () => {
    it("creates live badge that toggles visibility", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.live(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-live");
      expect(ctx.subscribe.on).toHaveBeenCalledWith("playing", expect.any(Function));
    });

    it("clicking label calls jumpToLive", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      DEFAULT_BLUEPRINTS.live(ctx);

      // The label button is created with addEventListener
      const buttons = createdElements.filter((e) => e.tagName === "BUTTON");
      const liveBtn = buttons.find((b) => b.textContent === "LIVE");
      expect(liveBtn).toBeDefined();
      liveBtn._fireEvent("click");
      expect(ctx.api.jumpToLive).toHaveBeenCalled();
    });
  });

  describe("currentTime", () => {
    it("creates time display that updates on subscription", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.currentTime(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-time");
      expect(el.textContent).toBe("0:00");
      expect(ctx.subscribe.on).toHaveBeenCalledWith("currentTime", expect.any(Function));
    });

    it("updates text on currentTime change", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.currentTime(ctx)!;

      fireSub(ctx, "currentTime", 65000);
      expect(el.textContent).not.toBe("0:00");
    });
  });

  describe("totalTime", () => {
    it("creates duration display", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.totalTime(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-duration");
      expect(ctx.subscribe.on).toHaveBeenCalledWith("duration", expect.any(Function));
    });

    it("shows empty for NaN duration", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.totalTime(ctx)!;

      fireSub(ctx, "duration", NaN);
      expect(el.textContent).toBe("");
    });

    it("shows empty for Infinity duration", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.totalTime(ctx)!;

      fireSub(ctx, "duration", Infinity);
      expect(el.textContent).toBe("");
    });

    it("formats valid duration", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.totalTime(ctx)!;

      fireSub(ctx, "duration", 120000);
      expect(el.textContent).not.toBe("");
      expect(el.textContent).not.toBe("0:00");
    });
  });

  describe("speaker", () => {
    it("creates mute button that toggles on click", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.speaker(ctx)!;

      expect(el.className).toContain("fw-bp-speaker");
      el._fireEvent("click");
      expect(ctx.api.toggleMute).toHaveBeenCalled();
    });

    it("subscribes to muted state", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      DEFAULT_BLUEPRINTS.speaker(ctx);
      expect(ctx.subscribe.on).toHaveBeenCalledWith("muted", expect.any(Function));
    });
  });

  describe("volume", () => {
    it("creates volume slider", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.volume(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-volume");
      expect(ctx.subscribe.on).toHaveBeenCalledWith("volume", expect.any(Function));
    });

    it("slider input sets api volume", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      DEFAULT_BLUEPRINTS.volume(ctx);

      const sliders = createdElements.filter((e) => e.type === "range");
      expect(sliders.length).toBeGreaterThan(0);
      const slider = sliders[0];
      slider.value = "0.7";
      slider._fireEvent("input");
      expect(ctx.api.volume).toBe(0.7);
    });
  });

  describe("fullscreen", () => {
    it("creates fullscreen button", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.fullscreen(ctx)!;

      expect(el.className).toContain("fw-bp-fullscreen");
      el._fireEvent("click");
      expect(ctx.api.toggleFullscreen).toHaveBeenCalled();
    });

    it("subscribes to fullscreen state", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      DEFAULT_BLUEPRINTS.fullscreen(ctx);
      expect(ctx.subscribe.on).toHaveBeenCalledWith("fullscreen", expect.any(Function));
    });
  });

  describe("pip", () => {
    it("creates PiP button", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.pip(ctx)!;

      expect(el.className).toContain("fw-bp-pip");
      el._fireEvent("click");
      expect(ctx.api.togglePiP).toHaveBeenCalled();
    });
  });

  describe("settings", () => {
    it("creates settings button that logs", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.settings(ctx)!;

      expect(el.className).toContain("fw-bp-settings");
      el._fireEvent("click");
      expect(ctx.log).toHaveBeenCalled();
    });
  });

  describe("progress", () => {
    it("creates progress bar that tracks time", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.progress(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-progress");
      expect(ctx.subscribe.on).toHaveBeenCalledWith("currentTime", expect.any(Function));
    });

    it("click seeks to position", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      ctx.api.duration = 100000;
      const el = DEFAULT_BLUEPRINTS.progress(ctx)!;

      el.getBoundingClientRect = vi.fn(() => ({ left: 0, width: 200 }));
      el._fireEvent("click", { clientX: 100 });
      expect(ctx.api.seek).toHaveBeenCalledWith(50000);
    });

    it("does not seek when duration is not finite", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      ctx.api.duration = NaN;
      const el = DEFAULT_BLUEPRINTS.progress(ctx)!;

      el.getBoundingClientRect = vi.fn(() => ({ left: 0, width: 200 }));
      el._fireEvent("click", { clientX: 100 });
      expect(ctx.api.seek).not.toHaveBeenCalled();
    });
  });

  describe("loading", () => {
    it("creates loading overlay", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.loading(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-loading");
      expect(ctx.subscribe.on).toHaveBeenCalledWith("buffering", expect.any(Function));
    });

    it("shows when buffering", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.loading(ctx)!;

      fireSub(ctx, "buffering", true);
      expect(el.style._map.get("display")).toBe("flex");
    });

    it("hides when not buffering", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.loading(ctx)!;

      fireSub(ctx, "buffering", false);
      expect(el.style._map.get("display")).toBe("none");
    });
  });

  describe("error", () => {
    it("creates error overlay", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.error(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-error");
      expect(ctx.subscribe.on).toHaveBeenCalledWith("error", expect.any(Function));
    });

    it("shows error message when error present", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.error(ctx)!;

      fireSub(ctx, "error", "Something went wrong");
      expect(el.style._map.get("display")).toBe("flex");
    });

    it("hides when error is null", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.error(ctx)!;

      fireSub(ctx, "error", null);
      expect(el.style._map.get("display")).toBe("none");
    });

    it("retry button clears error and retries", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      DEFAULT_BLUEPRINTS.error(ctx);

      const retryBtns = createdElements.filter(
        (e) => e.tagName === "BUTTON" && e.className.includes("fw-bp-error-retry")
      );
      expect(retryBtns.length).toBeGreaterThan(0);
      retryBtns[0]._fireEvent("click");

      expect(ctx.api.clearError).toHaveBeenCalled();
      expect(ctx.api.retry).toHaveBeenCalled();
    });
  });

  describe("spacer", () => {
    it("creates flex spacer", async () => {
      const { DEFAULT_BLUEPRINTS } = await getBlueprints();
      const ctx = makeCtx();
      const el = DEFAULT_BLUEPRINTS.spacer(ctx)!;

      expect(el).not.toBeNull();
      expect(el.className).toContain("fw-bp-spacer");
    });
  });
});
