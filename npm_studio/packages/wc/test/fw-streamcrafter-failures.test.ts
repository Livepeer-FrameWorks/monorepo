import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "../src/components/fw-streamcrafter.js";
import "../src/components/fw-sc-media-controls.js";
import type { FwStreamCrafter } from "../src/components/fw-streamcrafter.js";
import type { FwScMediaControls } from "../src/components/fw-sc-media-controls.js";
import {
  installStudioBrowser,
  lastPeer,
  whipRefuses,
  type StudioBrowserOptions,
} from "../../test-contract/studio-browser-fixture";

vi.mock("../../core/src/core/AudioMixer", async () => ({
  AudioMixer: (await import("../../test-contract/studio-browser-fixture")).FakeAudioMixer,
}));

const WHIP_URL = "https://edge.example/webrtc/key";
type StudioBrowser = ReturnType<typeof installStudioBrowser>;
let browser: StudioBrowser | null = null;
let element: FwStreamCrafter | null = null;

async function studio(options?: StudioBrowserOptions, props: Partial<FwStreamCrafter> = {}) {
  browser = browser ?? installStudioBrowser(options);
  const el = document.createElement("fw-streamcrafter") as FwStreamCrafter;
  el.whipUrl = WHIP_URL;
  el.enableCompositor = false;
  Object.assign(el, props);
  document.body.appendChild(el);
  element = el;
  await el.updateComplete;
  await picker(el).updateComplete;
  return { ...browser, el };
}

const root = (el: FwStreamCrafter) => el.shadowRoot!;
const picker = (el: FwStreamCrafter) => root(el).querySelector("fw-sc-camera-picker")!;
const addCamera = (el: FwStreamCrafter) =>
  picker(el).shadowRoot!.querySelector<HTMLButtonElement>('[title="Add Camera"]')!;
const screenButton = (el: FwStreamCrafter) =>
  root(el).querySelector<HTMLButtonElement>(".fw-sc-actions > button")!;
const goLive = (el: FwStreamCrafter) =>
  root(el).querySelector<HTMLButtonElement>(".fw-sc-action-primary")!;
const text = (el: FwStreamCrafter) => root(el).textContent ?? "";

async function settle(el: FwStreamCrafter) {
  await new Promise((resolve) => setTimeout(resolve, 20));
  await el.updateComplete;
}

async function startCamera(el: FwStreamCrafter) {
  addCamera(el).click();
  await vi.waitFor(async () => {
    await el.updateComplete;
    expect(goLive(el).disabled).toBe(false);
  });
}

beforeEach(() => {
  vi.spyOn(console, "log").mockImplementation(() => {});
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  element?.remove();
  element = null;
  vi.useRealTimers();
  browser?.uninstall();
  browser = null;
});

describe("a broadcast that fails", () => {
  it("shows why Go Live was refused", async () => {
    const { el, endpoint } = await studio();
    await startCamera(el);
    endpoint.respond = () => whipRefuses(403, "Forbidden", "stream key revoked");

    goLive(el).click();
    await settle(el);

    expect(text(el)).toContain("WHIP request failed: 403 Forbidden - stream key revoked");
    expect(goLive(el).disabled).toBe(false);
  });

  /** Go live, lose the connection and let the first attempt be refused. */
  async function reconnecting(el: FwStreamCrafter, endpoint: StudioBrowser["endpoint"]) {
    await startCamera(el);
    goLive(el).click();
    await settle(el);
    lastPeer().moveTo("connected");
    await el.updateComplete;
    vi.useFakeTimers();
    const attempts = vi.fn(() => whipRefuses(503, "Service Unavailable"));
    endpoint.respond = attempts;

    lastPeer().moveTo("failed");
    await advance(el, 1_500);
    return attempts;
  }

  async function advance(el: FwStreamCrafter, ms: number) {
    await vi.advanceTimersByTimeAsync(ms);
    await el.updateComplete;
  }

  const stopButton = (el: FwStreamCrafter) =>
    root(el).querySelector<HTMLButtonElement>(".fw-sc-action-stop");

  it("shows when the reconnection attempts run out, and keeps Go Live away until then", async () => {
    const { el, endpoint } = await studio();
    await reconnecting(el, endpoint);
    expect(goLive(el).textContent).not.toContain("Go Live");
    expect(text(el)).not.toContain("reconnection failed");

    await advance(el, 120_000);

    expect(text(el)).toContain("reconnection failed: WHIP request failed: 503");
    expect(goLive(el).disabled).toBe(false);
  });

  it("stops the retries for good from Stop while reconnecting", async () => {
    const { el, endpoint } = await studio();
    const attempts = await reconnecting(el, endpoint);
    expect(attempts).toHaveBeenCalledOnce();

    stopButton(el)!.click();
    await advance(el, 120_000);

    expect(attempts).toHaveBeenCalledOnce();
    expect(text(el)).toContain("Ready");
    expect(goLive(el).disabled).toBe(false);
    expect(text(el)).not.toContain("reconnection failed");
  });

  it("lets the broadcaster add a source again after a session with nothing left", async () => {
    const { el, endpoint } = await studio();
    await reconnecting(el, endpoint);
    root(el).querySelector<HTMLButtonElement>('[title="Remove source"]')!.click();
    await advance(el, 120_000);
    expect(el.pc.s.state).toBe("error");

    expect(addCamera(el).disabled).toBe(false);
    expect(screenButton(el).disabled).toBe(false);

    addCamera(el).click();
    await advance(el, 10);

    expect(text(el)).toContain("Ready");
    expect(goLive(el).disabled).toBe(false);
  });
});

describe("a machine with no webcam", () => {
  const noWebcam = async () => {
    throw new DOMException("Requested device not found", "NotFoundError");
  };

  it("says so and still lets the broadcaster share a screen", async () => {
    const { el } = await studio({ getUserMedia: noWebcam });

    addCamera(el).click();
    await settle(el);

    expect(text(el)).toContain("Requested device not found");
    expect(addCamera(el).disabled).toBe(false);
    expect(screenButton(el).disabled).toBe(false);

    screenButton(el).click();
    await vi.waitFor(async () => {
      await el.updateComplete;
      expect(goLive(el).disabled).toBe(false);
    });
  });
});

describe("a camera that will not open", () => {
  it("is reported once", async () => {
    const onError = vi.fn();
    const { el } = await studio(
      {
        getUserMedia: async () => {
          throw new DOMException("Requested device not found", "NotFoundError");
        },
      },
      { onError }
    );

    addCamera(el).click();
    await settle(el);

    expect(onError).toHaveBeenCalledOnce();
    expect(onError.mock.calls[0][0]).toMatch(/^getUserMedia failed: .*device not found/);
  });
});

describe("the camera list", () => {
  it("says so when the cameras cannot be listed", async () => {
    const { el } = await studio();
    Object.assign(el.pc, {
      getCameraChoices: async () => {
        throw new Error("enumerateDevices not supported");
      },
    });

    addCamera(el).click();
    await settle(el);

    expect(picker(el).shadowRoot!.querySelector('[role="alert"]')?.textContent?.trim()).toBe(
      "Could not list the cameras"
    );
  });

  it("clears that message on the next try", async () => {
    const { el } = await studio();
    const getCameraChoices = vi
      .fn()
      .mockRejectedValueOnce(new Error("enumerateDevices not supported"))
      .mockResolvedValueOnce([{ deviceId: "rear", label: "Rear", inUse: false }]);
    Object.assign(el.pc, { getCameraChoices });

    addCamera(el).click();
    await settle(el);
    addCamera(el).click();
    await settle(el);

    expect(picker(el).shadowRoot!.querySelector('[role="alert"]')).toBeNull();
  });
});

describe("a source without a layer in the active scene", () => {
  it("gets one when it is shown in the composition", async () => {
    const { el } = await studio();
    const addLayer = vi.fn();
    const setLayerVisibility = vi.fn();
    const scene = { id: "scene", layers: [] };
    Object.assign(el.pc, {
      getController: () => ({
        getSceneManager: () => ({
          getActiveScene: () => scene,
          addLayer,
          setLayerVisibility,
        }),
      }),
    });

    (
      el as unknown as { _toggleSourceLayerVisibility(sourceId: string): void }
    )._toggleSourceLayerVisibility("camera-1");

    expect(addLayer).toHaveBeenCalledWith("scene", "camera-1");
    expect(setLayerVisibility).not.toHaveBeenCalled();
  });
});

describe("a browser that cannot do everything", () => {
  it("leaves a browser that can do it all alone", async () => {
    const { el } = await studio();

    expect(root(el).querySelector(".fw-sc-error")).toBeNull();
    expect(addCamera(el).disabled).toBe(false);
    expect(screenButton(el).disabled).toBe(false);
  });

  it("explains that it cannot capture on an http origin", async () => {
    const { el } = await studio({ camera: false, screen: false });

    expect(text(el)).toContain("can't capture a camera or screen here");
    expect(addCamera(el).disabled).toBe(true);
    expect(screenButton(el).disabled).toBe(true);
  });

  it("does not try to start a camera it cannot have", async () => {
    const { el } = await studio({ camera: false, screen: false }, { autoStartCamera: true });

    await settle(el);

    expect(text(el)).not.toMatch(/Cannot read|undefined/);
  });

  it("explains that it cannot publish without WebRTC", async () => {
    const { el } = await studio({ publish: false });

    expect(text(el)).toContain("can't publish a stream");

    addCamera(el).click();
    await settle(el);
    expect(text(el)).toContain("Ready");
    expect(goLive(el).disabled).toBe(true);
  });

  it("turns off Share Screen on a phone and nothing else", async () => {
    const { el } = await studio({ screen: false });

    expect(screenButton(el).disabled).toBe(true);
    expect(screenButton(el).title).toBe("This browser can't share a screen");
    expect(addCamera(el).disabled).toBe(false);
    expect(root(el).querySelector(".fw-sc-error")).toBeNull();
  });

  it("says so when the compositor cannot start, and keeps the studio usable", async () => {
    const { el } = await studio(undefined, { enableCompositor: true, showProducerPanel: true });

    await settle(el);

    expect(text(el)).toContain("could not start the compositor");
    expect(addCamera(el).disabled).toBe(false);
  });

  it("tells nothing about the compositor when it was turned off", async () => {
    const { el } = await studio();

    await settle(el);

    expect(text(el)).not.toContain("could not start the compositor");
  });

  describe("adding a media file", () => {
    const section = async (el: FwStreamCrafter) => {
      const controls = document.createElement("fw-sc-media-controls") as FwScMediaControls;
      controls.studio = el;
      document.body.appendChild(controls);
      await controls.updateComplete;
      return controls.shadowRoot!;
    };

    it("is offered where the browser can play a file into the mix", async () => {
      const { el } = await studio();

      expect((await section(el)).querySelector('input[accept="video/*,audio/*"]')).toBeTruthy();
    });

    it("is replaced by a reason before a file is picked where it cannot", async () => {
      const { el } = await studio({ mediaFile: false });
      const host = await section(el);

      expect(host.querySelector('input[accept="video/*,audio/*"]')).toBeNull();
      expect(host.textContent).toContain("can't play a media file into the stream");
    });
  });
});
