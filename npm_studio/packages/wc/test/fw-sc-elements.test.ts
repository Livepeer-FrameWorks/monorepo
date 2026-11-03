import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReactiveControllerHost } from "lit";
import "../src/define.js";
import { FwStreamCrafter } from "../src/components/fw-streamcrafter.js";
import { FwScCameraPicker } from "../src/components/fw-sc-camera-picker.js";
import { FwScMediaControls } from "../src/components/fw-sc-media-controls.js";
import { FwScMediaButton } from "../src/components/fw-sc-media-button.js";
import { FwScOverlayControls } from "../src/components/fw-sc-overlay-controls.js";
import { FwScProducerPanel } from "../src/components/fw-sc-producer-panel.js";
import { FwScMeter } from "../src/components/fw-sc-meter.js";
import { FwScAudioControls } from "../src/components/fw-sc-audio-controls.js";
import { FwScConnectionHealth } from "../src/components/fw-sc-connection-health.js";
import { IngestControllerHost } from "../src/controllers/ingest-controller-host.js";
import { STUDIO_UPDATE_EVENT } from "../src/components/studio-bound-element.js";

afterEach(() => {
  document.body.innerHTML = "";
});

function fakeStudio(pc: Record<string, unknown>, sceneManager: unknown = null) {
  const studio = new FwStreamCrafter();
  Object.assign(studio.pc, pc);
  Object.defineProperty(studio, "sceneManager", { get: () => sceneManager });
  return studio;
}

describe("<fw-sc-camera-picker>", () => {
  it("lists the cameras with one live and starts the one picked", async () => {
    const startCamera = vi.fn(async () => ({}));
    const studio = fakeStudio({
      startCamera,
      getCameraChoices: async () => [
        { deviceId: "front", label: "Front", inUse: true },
        { deviceId: "rear", label: "Rear", inUse: false },
      ],
    });
    studio.pc.s.sources = [{ type: "camera" }] as never;
    const picker = new FwScCameraPicker();
    picker.studio = studio;

    await picker.toggle();
    expect(picker.choices).toHaveLength(2);
    expect(startCamera).not.toHaveBeenCalled();

    picker.pick("rear");
    expect(startCamera).toHaveBeenCalledWith({ videoDeviceId: "rear" });
    expect(picker.choices).toBeNull();
  });

  it("starts the only camera straight away", async () => {
    const startCamera = vi.fn(async () => ({}));
    const studio = fakeStudio({
      startCamera,
      getCameraChoices: async () => [{ deviceId: "front", label: "Front", inUse: false }],
    });
    const picker = new FwScCameraPicker();
    picker.studio = studio;

    await picker.toggle();

    expect(startCamera).toHaveBeenCalledWith({ videoDeviceId: "front" });
  });
});

describe("files the studio cannot use", () => {
  it("are reported by the overlay controls and the media button", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const scene = { id: "scene", layers: [] };
    const studio = fakeStudio(
      {
        addMediaFile: async () => {
          throw new Error("unsupported");
        },
      },
      {
        getActiveScene: () => scene,
        getOverlays: () => [],
        addImageOverlay: async () => {
          throw new Error("bad image");
        },
      }
    );
    const overlays = new FwScOverlayControls();
    overlays.studio = studio;
    const media = new FwScMediaButton();
    media.studio = studio;
    document.body.append(overlays, media);

    await overlays.addImage(new File(["x"], "logo.png"));
    await media.addFile(new File(["x"], "clip.mp4"));
    await overlays.updateComplete;
    await media.updateComplete;

    expect(overlays.shadowRoot?.querySelector('[role="alert"]')?.textContent).toBeTruthy();
    expect(media.shadowRoot?.querySelector('[role="alert"]')?.textContent).toBeTruthy();

    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, composed: true }));
    await media.updateComplete;

    expect(media.shadowRoot?.querySelector('[role="alert"]')).toBeNull();
    expect(media.shadowRoot?.querySelector("button")?.getAttribute("aria-label")).toBe(
      media.shadowRoot?.querySelector("button")?.getAttribute("title")
    );
  });
});

describe("sub-elements bound with for", () => {
  it("find a studio that mounts after them and render with it", async () => {
    const media = document.createElement("fw-sc-media-controls") as FwScMediaControls;
    media.setAttribute("for", "studio");
    document.body.append(media);
    await media.updateComplete;
    expect(media.shadowRoot?.querySelector(".fw-sc-producer-section")).toBeNull();

    const studio = document.createElement("fw-streamcrafter") as FwStreamCrafter;
    studio.id = "studio";
    document.body.append(studio);
    await studio.updateComplete;
    await media.updateComplete;

    expect(media.shadowRoot?.querySelector(".fw-sc-producer-section")).toBeTruthy();
  });

  it("follow `for` when it is pointed at another studio", async () => {
    const first = document.createElement("fw-streamcrafter") as FwStreamCrafter;
    first.id = "first";
    const second = document.createElement("fw-streamcrafter") as FwStreamCrafter;
    second.id = "second";
    document.body.append(first, second);
    const media = document.createElement("fw-sc-media-controls") as FwScMediaControls;
    media.setAttribute("for", "first");
    document.body.append(media);
    await media.updateComplete;
    const bound = () => (media as unknown as { boundStudio: FwStreamCrafter | null }).boundStudio;
    expect(bound()).toBe(first);

    media.setAttribute("for", "second");
    await media.updateComplete;

    expect(bound()).toBe(second);
  });

  it("ignore a studio with another id", async () => {
    const panel = document.createElement("fw-sc-producer-panel") as FwScProducerPanel;
    panel.setAttribute("for", "mine");
    document.body.append(panel);
    const other = document.createElement("fw-streamcrafter");
    other.id = "other";
    document.body.append(other);

    const requestUpdate = vi.spyOn(panel, "requestUpdate");
    other.dispatchEvent(new Event(STUDIO_UPDATE_EVENT, { bubbles: true, composed: true }));

    expect(requestUpdate).not.toHaveBeenCalled();
  });
});

describe("scenes from the keyboard", () => {
  const scenes = [
    {
      id: "one",
      name: "Default",
      layers: [{ id: "top", sourceId: "s", visible: true, zIndex: 0 }],
    },
    { id: "two", name: "Scene 2", layers: [] },
  ];

  it("go to the next scene on ] in the studio", async () => {
    const setActiveScene = vi.fn();
    const studio = document.createElement("fw-streamcrafter") as FwStreamCrafter;
    Object.defineProperty(studio, "sceneManager", {
      get: () => ({
        getAllScenes: () => scenes,
        getActiveScene: () => scenes[0],
        getCurrentLayout: () => ({ mode: "solo" }),
        setActiveScene,
        on: () => () => {},
      }),
    });
    document.body.append(studio);
    await studio.updateComplete;

    studio
      .shadowRoot!.querySelector(".fw-sc-root")!
      .dispatchEvent(new KeyboardEvent("keydown", { key: "]", bubbles: true, composed: true }));

    expect(setActiveScene).toHaveBeenCalledWith("two");
  });

  it("switch from a focused scene tile and remove a layer from the panel", async () => {
    const removeLayer = vi.fn();
    const setActiveScene = vi.fn();
    const studio = fakeStudio(
      {},
      {
        getAllScenes: () => scenes,
        getActiveScene: () => scenes[0],
        setActiveScene,
        transitionTo: vi.fn(async () => {}),
        removeLayer,
        getOverlays: () => [],
      }
    );
    const panel = new FwScProducerPanel();
    panel.studio = studio;
    document.body.append(panel);
    await panel.updateComplete;
    const switcher = panel.shadowRoot!.querySelector("fw-sc-scene-switcher")!;
    const layers = panel.shadowRoot!.querySelector("fw-sc-layer-list")!;
    await switcher.updateComplete;
    await layers.updateComplete;

    const selects = switcher.shadowRoot!.querySelectorAll<HTMLButtonElement>(".fw-sc-scene-select");
    expect(selects[1].tagName).toBe("BUTTON");
    expect(selects[1].getAttribute("aria-pressed")).toBe("false");
    expect(selects[1].querySelector("button")).toBeNull();
    selects[1].click();
    expect(
      (studio.sceneManager as unknown as { transitionTo: ReturnType<typeof vi.fn> }).transitionTo
    ).toHaveBeenCalledWith("two", expect.anything());

    layers.shadowRoot!.querySelector<HTMLButtonElement>('[title="Remove layer"]')!.click();
    expect(removeLayer).toHaveBeenCalledWith("one", "top");
  });
});

describe("audio levels", () => {
  function mixer() {
    let handler: ((event: { level: number; peakLevel: number }) => void) | null = null;
    return {
      on: (_event: string, fn: typeof handler) => {
        handler = fn;
        return () => {
          handler = null;
        };
      },
      startLevelMonitoring: () => {},
      stopLevelMonitoring: () => {},
      emit: (level: number) => handler?.({ level, peakLevel: level }),
    };
  }

  it("do not render the studio again", () => {
    const host = document.createElement("div") as unknown as ReactiveControllerHost & HTMLElement;
    const requestUpdate = vi.fn();
    Object.assign(host, { addController: vi.fn(), requestUpdate });
    const ic = new IngestControllerHost(host);
    const audio = mixer();
    Object.assign(ic, { controller: { getAudioMixer: () => audio } });

    (ic as unknown as { startAudioLevelMonitoring(): void }).startAudioLevelMonitoring();
    audio.emit(0.5);

    expect(requestUpdate).not.toHaveBeenCalled();
    expect(ic.s.audioLevel).toBe(0.5);
  });

  it("move the meter", async () => {
    const audio = mixer();
    const studio = fakeStudio({ getController: () => ({ getAudioMixer: () => audio }) });
    const meter = new FwScMeter();
    meter.studio = studio;
    document.body.append(meter);
    await meter.updateComplete;

    audio.emit(0.25);
    await meter.updateComplete;

    const fill = meter.shadowRoot?.querySelector<HTMLElement>(".fw-sc-vu-meter-fill");
    expect(fill?.getAttribute("style")).toContain("width:25%");
  });
});

describe("<fw-sc-audio-controls>", () => {
  function audioController() {
    const processing: Record<string, object> = {};
    return {
      setAudioProcessing: vi.fn((config: Record<string, object>) => {
        for (const [key, value] of Object.entries(config)) {
          processing[key] = { ...processing[key], ...value };
        }
      }),
      getAudioProcessing: () => ({ ...processing }),
      setAudioDucking: vi.fn(),
      isAudioDuckingEnabled: () => false,
    };
  }

  async function mount() {
    const controller = audioController();
    const studio = fakeStudio({ getController: () => controller });
    const controls = new FwScAudioControls();
    controls.studio = studio;
    controls.browserProcessing = {
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
    };
    document.body.append(controls);
    await controls.updateComplete;
    const root = controls.shadowRoot!;
    const toggle = (label: string) =>
      [...root.querySelectorAll<HTMLLabelElement>(".fw-sc-audio-toggle")]
        .find((el) => el.textContent?.includes(label))!
        .querySelector("input")!;
    return { controller, controls, toggle };
  }

  it("sets the gate, de-esser and ducking on the controller", async () => {
    const { controller, toggle } = await mount();

    toggle("Noise gate").click();
    toggle("De-esser").click();
    toggle("Lower other sources").click();

    expect(controller.setAudioProcessing).toHaveBeenCalledWith({
      noiseGate: { enabled: true, threshold: -40 },
    });
    expect(controller.setAudioProcessing).toHaveBeenCalledWith({ deEsser: { enabled: true } });
    expect(controller.setAudioDucking).toHaveBeenCalledWith(true);
  });

  it("asks the studio to change browser processing", async () => {
    const { controls, toggle } = await mount();
    const changes = vi.fn();
    controls.addEventListener("fw-audio-processing-change", (event) =>
      changes((event as CustomEvent).detail.settings)
    );

    toggle("Echo Cancellation").click();

    expect(changes).toHaveBeenCalledWith({ echoCancellation: false });
  });
});

describe("<fw-sc-connection-health>", () => {
  it("grades the connection while live", async () => {
    const studio = fakeStudio({});
    Object.assign(studio.pc.s, {
      isStreaming: true,
      stats: {
        video: { packetsSent: 1000, packetsLost: 0, bitrate: 1_000_000 },
        audio: { packetsSent: 0, packetsLost: 0, bitrate: 0 },
        connection: { rtt: 400, packetLossPct: 0 },
      },
    });
    const pill = new FwScConnectionHealth();
    pill.studio = studio;
    document.body.append(pill);
    await pill.updateComplete;

    expect(pill.shadowRoot!.querySelector('[role="status"]')?.textContent?.trim()).toBe(
      "Connection poor"
    );
  });
});

describe("presentational elements", () => {
  it("take no `for` attribute, since they do not bind to a studio", () => {
    for (const tag of [
      "fw-sc-volume",
      "fw-sc-compositor",
      "fw-sc-scene-switcher",
      "fw-sc-layer-list",
      "fw-sc-advanced",
    ]) {
      const ctor = customElements.get(tag) as unknown as {
        elementProperties: Map<string, unknown>;
      };
      expect(ctor.elementProperties.has("for"), tag).toBe(false);
    }
  });
});

describe("<fw-sc-advanced>", () => {
  it("leaves the browser processing toggles to the Audio section", async () => {
    const studio = fakeStudio({});
    const advanced = document.createElement("fw-sc-advanced") as HTMLElement & {
      ic: unknown;
      updateComplete: Promise<boolean>;
    };
    advanced.ic = studio.pc;
    document.body.append(advanced);
    await advanced.updateComplete;

    const text = advanced.shadowRoot!.textContent ?? "";
    expect(text).toContain("Sample Rate");
    expect(text).not.toContain("Echo Cancellation");
  });

  it("shows the encoder config read-only and marks the overrides", async () => {
    const studio = fakeStudio({});
    studio.pc.s.qualityProfile = "broadcast";
    const advanced = document.createElement("fw-sc-advanced") as HTMLElement & {
      ic: unknown;
      encoderOverrides: unknown;
      updateComplete: Promise<boolean>;
    };
    advanced.ic = studio.pc;
    advanced.encoderOverrides = { video: { width: 1280, height: 720 } };
    document.body.append(advanced);
    await advanced.updateComplete;
    const root = advanced.shadowRoot!;
    const infoTab = [...root.querySelectorAll("button")].find(
      (button) => button.textContent?.trim() === "Info"
    )!;
    infoTab.click();
    await advanced.updateComplete;

    expect(root.querySelector("select")).toBeNull();
    expect(root.textContent).not.toContain("Reset to Profile");
    const overridden = [...root.querySelectorAll("[data-overridden]")].map((row) =>
      row.textContent?.replace(/\s+/g, "")
    );
    expect(overridden).toEqual(["Resolution1280×720Overridden"]);
    expect(root.textContent).toContain("2.5 Mbps");
    expect(root.textContent).toContain("Change the encoder in Settings");
  });
});
