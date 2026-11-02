import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StreamCrafter } from "../src/components/StreamCrafter";
import { StudioActionBar } from "../src/components/StudioActionBar";
import { StreamCrafterProvider } from "../src/context/StreamCrafterContext";
import {
  installStudioBrowser,
  lastPeer,
  whipAccepts,
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
  render(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} {...props} />);
  return browser;
}

const addCamera = () => screen.getByTitle("Add Camera") as HTMLButtonElement;
const goLive = () => screen.getByText("Go Live") as HTMLButtonElement;
const screenButton = (container: HTMLElement) =>
  container.querySelectorAll<HTMLButtonElement>(".fw-sc-actions > button")[0];

async function startCamera() {
  await act(async () => {
    fireEvent.click(addCamera());
  });
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

    await act(async () => {
      fireEvent.click(goLive());
    });

    expect(
      screen.getByText(/WHIP request failed: 403 Forbidden - stream key revoked/)
    ).toBeTruthy();
    expect(goLive().disabled).toBe(false);
  });

  /** Go live, lose the connection and let the first attempt be refused. */
  async function reconnecting(endpoint: StudioBrowser["endpoint"]) {
    await startCamera();
    await act(async () => {
      fireEvent.click(goLive());
    });
    await act(async () => {
      lastPeer().moveTo("connected");
    });
    vi.useFakeTimers();
    const attempts = vi.fn(() => whipRefuses(503, "Service Unavailable"));
    endpoint.respond = attempts;

    await act(async () => {
      lastPeer().moveTo("failed");
    });
    await advance(1_500);
    return attempts;
  }

  async function advance(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  }

  it("shows when the reconnection attempts run out, and keeps Go Live away until then", async () => {
    const { endpoint } = studio();
    await reconnecting(endpoint);
    expect(screen.queryByText("Go Live")).toBeNull();
    expect(screen.queryByText(/reconnection failed/)).toBeNull();

    await advance(120_000);

    expect(screen.getByText(/reconnection failed: WHIP request failed: 503/)).toBeTruthy();
    expect(goLive().disabled).toBe(false);
    endpoint.respond = whipAccepts;
  });

  it("stops the retries for good from Stop while reconnecting", async () => {
    const { endpoint } = studio();
    const attempts = await reconnecting(endpoint);
    expect(attempts).toHaveBeenCalledOnce();

    fireEvent.click(screen.getByText("Stop Streaming"));
    await advance(120_000);

    expect(attempts).toHaveBeenCalledOnce();
    expect(screen.getByText(/^Ready$/)).toBeTruthy();
    expect(goLive().disabled).toBe(false);
    expect(screen.queryByText(/reconnection failed/)).toBeNull();
  });

  it("lets the broadcaster add a source again after a session with nothing left", async () => {
    const { endpoint } = studio();
    await reconnecting(endpoint);
    fireEvent.click(screen.getByTitle("Remove source"));
    await advance(120_000);
    expect(document.querySelector(".fw-sc-badge--error")).toBeTruthy();

    expect(addCamera().disabled).toBe(false);
    expect(screenButton(document.body).disabled).toBe(false);

    fireEvent.click(addCamera());
    await advance(10);

    expect(screen.getByText(/^Ready$/)).toBeTruthy();
    expect(goLive().disabled).toBe(false);
  });

  it("tells the host about it", async () => {
    const onError = vi.fn();
    const { endpoint } = studio(undefined, { onError });
    await startCamera();
    endpoint.respond = () => whipRefuses(503, "Service Unavailable");

    await act(async () => {
      fireEvent.click(goLive());
    });

    expect(onError).toHaveBeenCalledWith(expect.stringContaining("503"));
  });
});

describe("a machine with no webcam", () => {
  it("says so and still lets the broadcaster share a screen", async () => {
    browser = installStudioBrowser({
      getUserMedia: async () => {
        throw new DOMException("Requested device not found", "NotFoundError");
      },
    });
    const view = render(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} />);

    await act(async () => {
      fireEvent.click(addCamera());
    });

    expect(screen.getByText(/Requested device not found/)).toBeTruthy();
    expect(addCamera().disabled).toBe(false);
    expect(screenButton(view.container).disabled).toBe(false);

    await act(async () => {
      fireEvent.click(screenButton(view.container));
    });
    await waitFor(() => expect(goLive().disabled).toBe(false));
  });

  it("reports a camera that will not open once", async () => {
    const onError = vi.fn();

    studio(
      {
        getUserMedia: async () => {
          throw new DOMException("Requested device not found", "NotFoundError");
        },
      },
      { onError }
    );
    await act(async () => {
      fireEvent.click(addCamera());
    });

    expect(onError).toHaveBeenCalledOnce();
    expect(onError.mock.calls[0][0]).toMatch(/^getUserMedia failed: .*device not found/);
  });

  it("asks for the camera once when it starts by itself", async () => {
    const getUserMedia = vi.fn(async () => {
      throw new DOMException("Requested device not found", "NotFoundError");
    });

    studio({ getUserMedia }, { autoStartCamera: true });

    await waitFor(() => expect(screen.getByText(/Requested device not found/)).toBeTruthy());
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(getUserMedia).toHaveBeenCalledOnce();
  });
});

describe("a browser that cannot do everything", () => {
  it("leaves a browser that can do it all alone", () => {
    browser = installStudioBrowser();
    const view = render(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} />);

    expect(view.container.querySelector(".fw-sc-error")).toBeNull();
    expect(addCamera().disabled).toBe(false);
    expect(screenButton(view.container).disabled).toBe(false);
  });

  it("explains that it cannot capture on an http origin", () => {
    browser = installStudioBrowser({ camera: false, screen: false });
    const view = render(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} />);

    expect(screen.getByText(/can't capture a camera or screen here/)).toBeTruthy();
    expect(addCamera().disabled).toBe(true);
    expect(screenButton(view.container).disabled).toBe(true);
  });

  it("does not try to start a camera it cannot have", async () => {
    browser = installStudioBrowser({ camera: false, screen: false });
    render(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} autoStartCamera />);

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve));
    });

    expect(screen.queryByText(/Cannot read|undefined/)).toBeNull();
  });

  it("explains that it cannot publish without WebRTC", async () => {
    studio({ publish: false });

    expect(screen.getByText(/can't publish a stream/)).toBeTruthy();

    await act(async () => {
      fireEvent.click(addCamera());
    });
    await waitFor(() => expect(screen.getByText(/^Ready$/)).toBeTruthy());
    expect(goLive().disabled).toBe(true);
  });

  it("turns off Share Screen on a phone and nothing else", () => {
    browser = installStudioBrowser({ screen: false });
    const view = render(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} />);

    const share = screenButton(view.container);
    expect(share.disabled).toBe(true);
    expect(share.title).toBe("This browser can't share a screen");
    expect(addCamera().disabled).toBe(false);
    expect(view.container.querySelector(".fw-sc-error")).toBeNull();
  });

  it("says so when the compositor cannot start, and keeps the studio usable", async () => {
    browser = installStudioBrowser();
    render(<StreamCrafter whipUrl={WHIP_URL} showProducerPanel />);

    expect(await screen.findByText(/could not start the compositor/)).toBeTruthy();
    expect(addCamera().disabled).toBe(false);
  });

  it("tells nothing about the compositor when it was turned off", async () => {
    studio();

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve));
    });

    expect(screen.queryByText(/could not start the compositor/)).toBeNull();
  });

  it("gives the same controls in the composable action bar", () => {
    browser = installStudioBrowser({ camera: true, screen: false, publish: false });
    const context = {
      state: "capturing",
      isCapturing: true,
      isStreaming: false,
      sources: [],
      getCameraChoices: async () => [],
    } as never;

    const view = render(
      <StreamCrafterProvider value={context}>
        <StudioActionBar whipUrl={WHIP_URL} showSettingsButton={false} />
      </StreamCrafterProvider>
    );

    expect(screenButton(view.container).disabled).toBe(true);
    expect(goLive().disabled).toBe(true);
  });

  it("offers Stop in the composable action bar while reconnecting", async () => {
    browser = installStudioBrowser();
    const stopStreaming = vi.fn(async () => {});
    const context = {
      state: "reconnecting",
      isCapturing: false,
      isStreaming: false,
      isReconnecting: true,
      sources: [],
      getCameraChoices: async () => [],
      stopStreaming,
    } as never;

    render(
      <StreamCrafterProvider value={context}>
        <StudioActionBar whipUrl={WHIP_URL} showSettingsButton={false} />
      </StreamCrafterProvider>
    );
    await act(async () => {
      fireEvent.click(screen.getByText("Stop Streaming"));
    });

    expect(stopStreaming).toHaveBeenCalledOnce();
  });

  it("keeps sources available in the composable action bar after an error", () => {
    browser = installStudioBrowser();
    const context = {
      state: "error",
      isCapturing: false,
      isStreaming: false,
      isReconnecting: false,
      sources: [],
      getCameraChoices: async () => [],
    } as never;

    const view = render(
      <StreamCrafterProvider value={context}>
        <StudioActionBar whipUrl={WHIP_URL} showSettingsButton={false} />
      </StreamCrafterProvider>
    );

    expect(addCamera().disabled).toBe(false);
    expect(screenButton(view.container).disabled).toBe(false);
  });

  describe("adding a media file", () => {
    const addMediaFile = vi.fn(async () => {
      throw new Error("unsupported codec");
    });
    const bar = () =>
      render(
        <StreamCrafterProvider
          value={
            {
              state: "idle",
              isCapturing: false,
              isStreaming: false,
              isReconnecting: false,
              sources: [],
              getCameraChoices: async () => [],
              addMediaFile,
            } as never
          }
        >
          <StudioActionBar whipUrl={WHIP_URL} showSettingsButton={false} />
        </StreamCrafterProvider>
      );

    it("sits in the action bar next to Add Camera", () => {
      browser = installStudioBrowser();
      bar();

      expect((screen.getByTitle("Add media file") as HTMLButtonElement).disabled).toBe(false);
    });

    it("is turned off with the reason where the browser cannot play a file into the mix", () => {
      browser = installStudioBrowser({ mediaFile: false });
      bar();

      const button = screen.getByTitle(
        "This browser can't play a media file into the stream."
      ) as HTMLButtonElement;
      expect(button.disabled).toBe(true);
    });

    it("says so when a file cannot be played", async () => {
      browser = installStudioBrowser();
      const view = bar();
      const input = view.container.querySelector<HTMLInputElement>(
        'input[accept="video/*,audio/*"]'
      )!;

      await act(async () => {
        fireEvent.change(input, { target: { files: [new File(["x"], "clip.mkv")] } });
      });

      expect(addMediaFile).toHaveBeenCalledOnce();
      expect(screen.getByRole("alert").textContent).toContain("could not be played");
    });
  });
});
