import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";
import { StudioLayoutKeeper, type StudioLayoutHost } from "../src/core/StudioLayoutKeeper";
import { createDefaultStudioLayout, type StudioLayout } from "../src/core/studioLayout";

const whip = vi.hoisted(() => ({
  constructed: vi.fn(),
  connect: vi.fn(),
  disconnect: vi.fn(),
  destroy: vi.fn(),
}));
vi.mock("../src/core/WhipClient", () => ({
  WhipClient: class {
    constructor(config: unknown) {
      whip.constructed(config);
    }
    on() {
      return () => {};
    }
    connect = whip.connect;
    disconnect = whip.disconnect;
    destroy = whip.destroy;
  },
}));

class FakeMediaStream {
  constructor(private tracks: MediaStreamTrack[] = []) {}
  getTracks() {
    return this.tracks;
  }
  getVideoTracks() {
    return this.tracks.filter((track) => track.kind === "video");
  }
  getAudioTracks() {
    return this.tracks.filter((track) => track.kind === "audio");
  }
}

function layout(name: string): StudioLayout {
  return {
    version: 1,
    layout: { mode: "solo" },
    activeScene: 0,
    scenes: [{ name, backgroundColor: "#000000" }],
    overlays: [],
  };
}

/** A controller whose compositor shows the last layout it imported. */
class FakeHost implements StudioLayoutHost {
  enabled = false;
  shown: StudioLayout = createDefaultStudioLayout();
  stopped: StudioLayout | null = null;
  imports: StudioLayout[] = [];
  importDelay: Promise<void> | null = null;
  exportDelay: Promise<void> | null = null;
  rejects = new Set<StudioLayout>();
  async enable() {
    this.enabled = true;
    this.shown = createDefaultStudioLayout();
  }
  disable() {
    this.stopped = this.shown;
    this.enabled = false;
  }
  isCompositorEnabled() {
    return this.enabled;
  }
  async exportLayout() {
    const shown = this.enabled ? this.shown : this.stopped;
    await this.exportDelay;
    return shown;
  }
  async importLayout(next: StudioLayout) {
    this.imports.push(next);
    await this.importDelay;
    if (this.rejects.has(next)) throw new Error("Unsupported studio layout version");
    this.shown = next;
  }
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((r) => (resolve = r));
  return { promise, resolve };
}

describe("IngestControllerV2.setWhipUrl", () => {
  const controllers: IngestControllerV2[] = [];
  function create() {
    const controller = new IngestControllerV2({
      whipUrl: "https://edge.example/webrtc/a",
      useWebCodecs: false,
    });
    controllers.push(controller);
    const track = {
      kind: "video",
      id: "track",
      enabled: true,
      stop: vi.fn(),
      addEventListener: vi.fn(),
    } as unknown as MediaStreamTrack;
    const source = controller.addCustomSource(
      new FakeMediaStream([track]) as unknown as MediaStream,
      "Camera fixture"
    );
    return { controller, track, source };
  }

  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal("MediaStream", FakeMediaStream);
  });
  afterEach(() => {
    controllers.splice(0).forEach((controller) => controller.destroy());
    vi.unstubAllGlobals();
  });

  it("sends the next connection to the new endpoint and keeps the sources", async () => {
    const { controller, source, track } = create();

    await controller.setWhipUrl("https://edge.example/webrtc/b");
    await controller.startStreaming();

    expect(whip.constructed.mock.calls.map(([config]) => config.whipUrl)).toEqual([
      "https://edge.example/webrtc/b",
    ]);
    expect(controller.getSources().map((s) => s.id)).toEqual([source.id]);
    expect(track.stop).not.toHaveBeenCalled();
  });

  it("stops publishing to the old endpoint and keeps capturing", async () => {
    const { controller, track } = create();
    await controller.startStreaming();
    expect(controller.getState()).not.toBe("capturing");

    await controller.setWhipUrl("https://edge.example/webrtc/b");

    expect(whip.disconnect).toHaveBeenCalledTimes(1);
    expect(controller.getState()).toBe("capturing");
    expect(track.stop).not.toHaveBeenCalled();
    await controller.startStreaming();
    expect(whip.constructed.mock.calls.at(-1)?.[0].whipUrl).toBe("https://edge.example/webrtc/b");
  });

  it("leaves a live connection alone when the endpoint is the same", async () => {
    const { controller } = create();
    await controller.startStreaming();
    const state = controller.getState();

    await controller.setWhipUrl("https://edge.example/webrtc/a");

    expect(whip.disconnect).not.toHaveBeenCalled();
    expect(controller.getState()).toBe(state);
  });
});

describe("StudioLayoutKeeper", () => {
  let warn: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  });
  afterEach(() => {
    vi.useRealTimers();
    warn.mockRestore();
  });

  async function started(keeper: StudioLayoutKeeper<FakeHost>, host = new FakeHost()) {
    await keeper.start(host, () => host.enable());
    return host;
  }

  it("starts from the layout given for the key", async () => {
    const keeper = new StudioLayoutKeeper<FakeHost>();
    keeper.setLayout("a", layout("A"));
    const host = await started(keeper);
    expect(host.shown).toEqual(layout("A"));
  });

  it("shows the next stream's layout in place on a key change", async () => {
    const keeper = new StudioLayoutKeeper<FakeHost>();
    keeper.setLayout("a", layout("A"));
    const host = await started(keeper);

    expect(keeper.setLayout("b", layout("B"))).toBe(true);
    await keeper.apply(host);

    expect(host.shown).toEqual(layout("B"));
    expect(host.enabled).toBe(true);
  });

  it("resets to the default layout for a stream without one, even when neither has one", async () => {
    const keeper = new StudioLayoutKeeper<FakeHost>();
    keeper.setLayout("a", null);
    const host = await started(keeper);
    host.shown = layout("A, edited");

    keeper.setLayout("b", null);
    await keeper.apply(host);

    expect(host.shown).toEqual(createDefaultStudioLayout());
  });

  it("resets to the default layout when the next stream's layout does not apply", async () => {
    const keeper = new StudioLayoutKeeper<FakeHost>();
    keeper.setLayout("a", layout("A"));
    const host = await started(keeper);
    const broken = layout("B");
    host.rejects.add(broken);

    keeper.setLayout("b", broken);
    await keeper.apply(host);

    expect(host.shown).toEqual(createDefaultStudioLayout());
  });

  it("keeps the layout when only the layout object changes", async () => {
    const keeper = new StudioLayoutKeeper<FakeHost>();
    keeper.setLayout("a", layout("A"));
    const host = await started(keeper);
    host.shown = layout("A, edited");

    expect(keeper.setLayout("a", layout("A, reloaded"))).toBe(false);
    await keeper.apply(host);

    expect(host.shown).toEqual(layout("A, edited"));
  });

  it("carries the layout to a new controller for the same key", async () => {
    const keeper = new StudioLayoutKeeper<FakeHost>();
    keeper.setLayout("a", layout("A"));
    const first = await started(keeper);
    first.shown = layout("A, edited");
    first.disable();

    const second = await started(keeper);

    expect(second.shown).toEqual(layout("A, edited"));
  });

  it("does not carry one stream's layout to another stream's start", async () => {
    const keeper = new StudioLayoutKeeper<FakeHost>();
    keeper.setLayout("a", null);
    const host = await started(keeper);
    host.shown = layout("A, edited");
    host.disable();

    keeper.setLayout("b", null);
    await keeper.apply(host);
    await keeper.start(host, () => host.enable());

    expect(host.shown).toEqual(createDefaultStudioLayout());
  });

  it("applies the newest key when it changes while the compositor starts", async () => {
    const keeper = new StudioLayoutKeeper<FakeHost>();
    keeper.setLayout("a", layout("A"));
    const host = new FakeHost();
    const enabling = deferred();
    const start = keeper.start(host, async () => {
      await enabling.promise;
      await host.enable();
    });

    keeper.setLayout("b", layout("B"));
    const switching = keeper.apply(host);
    enabling.resolve();
    await Promise.all([start, switching]);

    expect(host.shown).toEqual(layout("B"));
  });

  it("reports a layout with the key it was exported under", async () => {
    vi.useFakeTimers();
    const keeper = new StudioLayoutKeeper<FakeHost>(300);
    keeper.setLayout("a", layout("A"));
    const host = await started(keeper);
    const report = vi.fn();

    keeper.scheduleReport(host, report);
    await vi.advanceTimersByTimeAsync(300);

    expect(report).toHaveBeenCalledWith(layout("A"), "a");
  });

  it("drops a pending report when the key changes", async () => {
    vi.useFakeTimers();
    const keeper = new StudioLayoutKeeper<FakeHost>(300);
    keeper.setLayout("a", layout("A"));
    const host = await started(keeper);
    const report = vi.fn();

    keeper.scheduleReport(host, report);
    keeper.setLayout("b", null);
    await keeper.apply(host);
    await vi.advanceTimersByTimeAsync(300);

    expect(report).not.toHaveBeenCalled();
  });

  it("drops an export in flight when the key changes", async () => {
    vi.useFakeTimers();
    const keeper = new StudioLayoutKeeper<FakeHost>(300);
    keeper.setLayout("a", layout("A"));
    const host = await started(keeper);
    const exporting = deferred();
    host.exportDelay = exporting.promise;
    const report = vi.fn();

    keeper.scheduleReport(host, report);
    await vi.advanceTimersByTimeAsync(300);
    keeper.setLayout("b", null);
    exporting.resolve();
    await vi.advanceTimersByTimeAsync(0);

    expect(report).not.toHaveBeenCalled();
  });

  it("reports the next stream's layout only after it was applied", async () => {
    vi.useFakeTimers();
    const keeper = new StudioLayoutKeeper<FakeHost>(300);
    keeper.setLayout("a", layout("A"));
    const host = await started(keeper);
    const importing = deferred();
    host.importDelay = importing.promise;
    const report = vi.fn();

    keeper.setLayout("b", layout("B"));
    const switching = keeper.apply(host);
    keeper.scheduleReport(host, report);
    await vi.advanceTimersByTimeAsync(300);
    expect(report).not.toHaveBeenCalled();

    importing.resolve();
    await switching;
    await vi.advanceTimersByTimeAsync(0);

    expect(report).toHaveBeenCalledTimes(1);
    expect(report).toHaveBeenCalledWith(layout("B"), "b");
  });
});
