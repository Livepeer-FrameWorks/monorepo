import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { tick } from "svelte";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { readable } from "svelte/store";
import { createStudioTranslator, type MediaSource } from "@livepeer-frameworks/streamcrafter-core";
import StreamCrafter from "../src/StreamCrafter.svelte";
import StudioActionBar from "../src/components/StudioActionBar.svelte";
import StudioMediaControls from "../src/components/StudioMediaControls.svelte";
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

function studio(options?: StudioBrowserOptions, props: Record<string, unknown> = {}) {
  browser = installStudioBrowser(options);
  const view = render(StreamCrafter, { whipUrl: WHIP_URL, enableCompositor: false, ...props });
  return { ...browser, ...view };
}

const addCamera = () => screen.getByTitle("Add Camera") as HTMLButtonElement;
const goLive = () => screen.getByText("Go Live") as HTMLButtonElement;
const screenButton = (container: HTMLElement) =>
  container.querySelectorAll<HTMLButtonElement>(".fw-sc-actions > button")[0];
const settled = () => new Promise((resolve) => setTimeout(resolve, 20));

async function startCamera() {
  await waitFor(() => expect(addCamera().disabled).toBe(false));
  await fireEvent.click(addCamera());
  await waitFor(() => expect(goLive().disabled).toBe(false));
}

beforeEach(() => {
  vi.spyOn(console, "log").mockImplementation(() => {});
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  browser?.uninstall();
  browser = null;
});

describe("a broadcast that fails", () => {
  it("shows why Go Live was refused", async () => {
    const { endpoint } = studio();
    await startCamera();
    endpoint.respond = () => whipRefuses(403, "Forbidden", "stream key revoked");

    await fireEvent.click(goLive());

    expect(
      await screen.findByText(/WHIP request failed: 403 Forbidden - stream key revoked/)
    ).toBeTruthy();
    expect(goLive().disabled).toBe(false);
  });

  /** Go live, lose the connection and let the first attempt be refused. */
  async function reconnecting(endpoint: StudioBrowser["endpoint"]) {
    await startCamera();
    await fireEvent.click(goLive());
    await waitFor(() => expect(lastPeer()).toBeTruthy());
    await settled();
    lastPeer().moveTo("connected");
    await waitFor(() => expect(screen.getByText("Stop Streaming")).toBeTruthy());
    vi.useFakeTimers();
    const attempts = vi.fn(() => whipRefuses(503, "Service Unavailable"));
    endpoint.respond = attempts;

    lastPeer().moveTo("failed");
    await advance(1_500);
    return attempts;
  }

  async function advance(ms: number) {
    await vi.advanceTimersByTimeAsync(ms);
    await tick();
  }

  it("shows when the reconnection attempts run out, and keeps Go Live away until then", async () => {
    const { endpoint } = studio();
    await reconnecting(endpoint);
    expect(screen.queryByText("Go Live")).toBeNull();
    expect(screen.queryByText(/reconnection failed/)).toBeNull();

    await advance(120_000);

    expect(screen.getByText(/reconnection failed: WHIP request failed: 503/)).toBeTruthy();
    expect(goLive().disabled).toBe(false);
  });

  it("stops the retries for good from Stop while reconnecting", async () => {
    const { endpoint } = studio();
    const attempts = await reconnecting(endpoint);
    expect(attempts).toHaveBeenCalledOnce();

    await fireEvent.click(screen.getByText("Stop Streaming"));
    await advance(120_000);

    expect(attempts).toHaveBeenCalledOnce();
    expect(screen.getByText(/^Ready$/)).toBeTruthy();
    expect(goLive().disabled).toBe(false);
    expect(screen.queryByText(/reconnection failed/)).toBeNull();
  });

  it("lets the broadcaster add a source again after a session with nothing left", async () => {
    const { endpoint, container } = studio();
    await reconnecting(endpoint);
    await fireEvent.click(screen.getByTitle("Remove source"));
    await advance(120_000);
    expect(container.querySelector(".fw-sc-badge--error")).toBeTruthy();

    expect(addCamera().disabled).toBe(false);
    expect(screenButton(container).disabled).toBe(false);

    await fireEvent.click(addCamera());
    await advance(10);

    expect(screen.getByText(/^Ready$/)).toBeTruthy();
    expect(goLive().disabled).toBe(false);
  });
});

describe("a machine with no webcam", () => {
  const noWebcam = async () => {
    throw new DOMException("Requested device not found", "NotFoundError");
  };

  it("says so and still lets the broadcaster share a screen", async () => {
    const { container } = studio({ getUserMedia: noWebcam });

    await waitFor(() => expect(addCamera().disabled).toBe(false));
    await fireEvent.click(addCamera());

    expect(await screen.findByText(/Requested device not found/)).toBeTruthy();
    expect(addCamera().disabled).toBe(false);
    expect(screenButton(container).disabled).toBe(false);

    await fireEvent.click(screenButton(container));
    await waitFor(() => expect(goLive().disabled).toBe(false));
  });

  it("reports a camera that will not open once", async () => {
    const onError = vi.fn();
    studio({ getUserMedia: noWebcam }, { onError });

    await waitFor(() => expect(addCamera().disabled).toBe(false));
    await fireEvent.click(addCamera());
    await settled();

    expect(onError).toHaveBeenCalledOnce();
    expect(onError.mock.calls[0][0]).toMatch(/^getUserMedia failed: .*device not found/);
  });

  it("asks for the camera once when it starts by itself", async () => {
    const getUserMedia = vi.fn(noWebcam);

    studio({ getUserMedia }, { autoStartCamera: true });

    await screen.findByText(/Requested device not found/);
    await settled();
    expect(getUserMedia).toHaveBeenCalledOnce();
  });
});

describe("a browser that cannot do everything", () => {
  it("leaves a browser that can do it all alone", async () => {
    const { container } = studio();
    await tick();

    expect(container.querySelector(".fw-sc-error")).toBeNull();
    expect(addCamera().disabled).toBe(false);
    expect(screenButton(container).disabled).toBe(false);
  });

  it("explains that it cannot capture on an http origin", async () => {
    const { container } = studio({ camera: false, screen: false });

    expect(await screen.findByText(/can't capture a camera or screen here/)).toBeTruthy();
    expect(addCamera().disabled).toBe(true);
    expect(screenButton(container).disabled).toBe(true);
  });

  it("does not try to start a camera it cannot have", async () => {
    studio({ camera: false, screen: false }, { autoStartCamera: true });

    await settled();

    expect(screen.queryByText(/Cannot read|undefined/)).toBeNull();
  });

  it("explains that it cannot publish without WebRTC", async () => {
    studio({ publish: false });

    expect(await screen.findByText(/can't publish a stream/)).toBeTruthy();

    await fireEvent.click(addCamera());
    await screen.findByText(/^Ready$/);
    expect(goLive().disabled).toBe(true);
  });

  it("turns off Share Screen on a phone and nothing else", async () => {
    const { container } = studio({ screen: false });

    await waitFor(() => expect(screenButton(container).disabled).toBe(true));
    expect(screenButton(container).title).toBe("This browser can't share a screen");
    expect(addCamera().disabled).toBe(false);
    expect(container.querySelector(".fw-sc-error")).toBeNull();
  });

  it("says so when the compositor cannot start, and keeps the studio usable", async () => {
    browser = installStudioBrowser();
    render(StreamCrafter, { whipUrl: WHIP_URL, showProducerPanel: true });

    expect(await screen.findByText(/could not start the compositor/)).toBeTruthy();
    expect(addCamera().disabled).toBe(false);
  });

  it("tells nothing about the compositor when it was turned off", async () => {
    studio();

    await settled();

    expect(screen.queryByText(/could not start the compositor/)).toBeNull();
  });

  it("gives the same controls in the composable action bar", async () => {
    browser = installStudioBrowser({ screen: false, publish: false });
    const context = new Map<string, unknown>([
      [
        "fw-sc-controller",
        {
          state: "capturing",
          isCapturing: true,
          isStreaming: false,
          sources: [] as MediaSource[],
          getCameraChoices: async () => [],
        },
      ],
    ]);

    const { container } = render(StudioActionBar, {
      props: { whipUrl: WHIP_URL, showSettingsButton: false },
      context,
    });

    await waitFor(() => expect(screenButton(container).disabled).toBe(true));
    expect(goLive().disabled).toBe(true);
  });

  it("offers Stop in the composable action bar while reconnecting", async () => {
    browser = installStudioBrowser();
    const stopStreaming = vi.fn(async () => {});
    const context = new Map<string, unknown>([
      [
        "fw-sc-controller",
        {
          state: "reconnecting",
          isCapturing: false,
          isStreaming: false,
          isReconnecting: true,
          sources: [] as MediaSource[],
          getCameraChoices: async () => [],
          stopStreaming,
        },
      ],
    ]);

    render(StudioActionBar, {
      props: { whipUrl: WHIP_URL, showSettingsButton: false },
      context,
    });
    await fireEvent.click(screen.getByText("Stop Streaming"));

    expect(stopStreaming).toHaveBeenCalledOnce();
  });

  it("keeps sources available in the composable action bar after an error", async () => {
    browser = installStudioBrowser();
    const context = new Map<string, unknown>([
      [
        "fw-sc-controller",
        {
          state: "error",
          isCapturing: false,
          isStreaming: false,
          isReconnecting: false,
          sources: [] as MediaSource[],
          getCameraChoices: async () => [],
        },
      ],
    ]);

    const { container } = render(StudioActionBar, {
      props: { whipUrl: WHIP_URL, showSettingsButton: false },
      context,
    });
    await tick();

    expect(addCamera().disabled).toBe(false);
    expect(screenButton(container).disabled).toBe(false);
  });

  describe("adding a media file", () => {
    const controls = () =>
      render(StudioMediaControls, {
        props: {
          sources: [],
          addMediaFile: async () => {},
          playMediaSource: () => {},
          pauseMediaSource: () => {},
          setMediaSourceLoop: () => {},
        },
        context: new Map([
          ["fw-sc-translator", readable(createStudioTranslator({ locale: "en" }))],
        ]),
      });

    it("is offered where the browser can play a file into the mix", async () => {
      browser = installStudioBrowser();
      const { container } = controls();
      await tick();

      expect(container.querySelector('input[accept="video/*,audio/*"]')).toBeTruthy();
    });

    it("is replaced by a reason before a file is picked where it cannot", async () => {
      browser = installStudioBrowser({ mediaFile: false });
      const { container } = controls();

      expect(await screen.findByText(/can't play a media file into the stream/)).toBeTruthy();
      expect(container.querySelector('input[accept="video/*,audio/*"]')).toBeNull();
    });
  });
});
