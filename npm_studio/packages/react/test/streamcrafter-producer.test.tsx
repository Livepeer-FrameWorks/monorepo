import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

const controllers = vi.hoisted(() => [] as { enableCompositor: ReturnType<typeof vi.fn> }[]);
const sceneSwitches = vi.hoisted(() => vi.fn());

vi.mock("@livepeer-frameworks/streamcrafter-core", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();

  class FakeController {
    private sceneManager: unknown = null;
    enableCompositor = vi.fn(async () => {
      const scene = { id: "scene-1", name: "Default", layers: [], backgroundColor: "#000000" };
      const second = { id: "scene-2", name: "Scene 2", layers: [], backgroundColor: "#000000" };
      this.sceneManager = {
        on: () => () => {},
        getAllScenes: () => [scene, second],
        getActiveScene: () => scene,
        setActiveScene: sceneSwitches,
        getRendererType: () => "canvas2d",
        getStats: () => ({ fps: 30, frameTimeMs: 1 }),
        getCurrentLayout: () => ({ mode: "solo" }),
        getOverlays: () => [],
        isInitialized: () => true,
      };
    });
    constructor() {
      controllers.push(this);
    }
    on() {
      return () => {};
    }
    getSceneManager() {
      return this.sceneManager;
    }
    getAudioMixer() {
      return {
        on: () => () => {},
        startLevelMonitoring: () => {},
        stopLevelMonitoring: () => {},
        setAudioProcessing: () => {},
      };
    }
    getMediaStream() {
      return null;
    }
    getSources() {
      return [];
    }
    setEncoderOverrides() {}
    setAudioProcessing() {}
    getAudioProcessing() {
      return {};
    }
    destroy() {}
    disableCompositor() {}
    getEncoderManager() {
      return null;
    }
    isWebCodecsActive() {
      return false;
    }
  }

  return { ...actual, IngestControllerV2: FakeController };
});

import { StreamCrafter } from "../src/components/StreamCrafter";
import { StudioProducerPanel } from "../src/components/StudioProducerPanel";
import { StudioOverlayControls } from "../src/components/StudioOverlayControls";

const WHIP_URL = "https://ingest.example/whip";

beforeEach(() => {
  controllers.length = 0;
  sceneSwitches.mockClear();
  vi.spyOn(console, "log").mockImplementation(() => {});
});

afterEach(cleanup);

describe("StreamCrafter producer panel", () => {
  it("is on by default once the compositor runs", async () => {
    render(<StreamCrafter whipUrl={WHIP_URL} />);

    expect(await screen.findByText("Scenes")).toBeTruthy();
    expect(screen.getByText("Default")).toBeTruthy();
  });

  it("goes to the next scene on ] and leaves keys typed into a field alone", async () => {
    const { container } = render(<StreamCrafter whipUrl={WHIP_URL} />);
    await screen.findByText("Scenes");
    const root = container.querySelector(".fw-sc-root") as HTMLElement;

    fireEvent.keyDown(container.querySelector("textarea, input") ?? root, { key: "]" });
    fireEvent.keyDown(root, { key: "]" });

    expect(sceneSwitches).toHaveBeenCalledTimes(1);
    expect(sceneSwitches).toHaveBeenCalledWith("scene-2");
  });

  it("can be turned off", async () => {
    const { container } = render(<StreamCrafter whipUrl={WHIP_URL} showProducerPanel={false} />);

    await waitFor(() => expect(controllers[0]?.enableCompositor).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve));

    expect(container.querySelector(".fw-sc-producer")).toBeNull();
  });

  it("gives composable children the compositor through context", async () => {
    render(
      <StreamCrafter whipUrl={WHIP_URL}>
        <StudioProducerPanel />
        <StudioOverlayControls />
      </StreamCrafter>
    );

    expect(await screen.findByText("Default")).toBeTruthy();
    expect(screen.getAllByText("Overlays")).toHaveLength(2);
  });

  it("stays away when the compositor is turned off", async () => {
    const { container } = render(<StreamCrafter whipUrl={WHIP_URL} enableCompositor={false} />);

    await waitFor(() => expect(controllers).toHaveLength(1));
    expect(container.querySelector(".fw-sc-producer")).toBeNull();
    expect(controllers[0].enableCompositor).not.toHaveBeenCalled();
  });
});
