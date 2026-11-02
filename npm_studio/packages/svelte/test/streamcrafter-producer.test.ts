import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";

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
      };
    }
    getMediaStream() {
      return null;
    }
    getSources() {
      return [];
    }
    destroy() {}
    getEncoderManager() {
      return null;
    }
    isWebCodecsActive() {
      return false;
    }
    isCompositorEnabled() {
      return this.sceneManager !== null;
    }
  }

  return { ...actual, IngestControllerV2: FakeController };
});

import StreamCrafter from "../src/StreamCrafter.svelte";
import ProducerPanelHost from "./fixtures/ProducerPanelHost.svelte";

describe("StreamCrafter producer panel", () => {
  beforeEach(() => {
    controllers.length = 0;
    sceneSwitches.mockClear();
    vi.spyOn(console, "log").mockImplementation(() => {});
  });

  it("goes to the next scene on ]", async () => {
    const { container } = render(StreamCrafter, { whipUrl: "https://ingest.example/whip" });
    await screen.findByText("Scenes");

    await fireEvent.keyDown(container.querySelector(".fw-sc-root") as HTMLElement, { key: "]" });

    expect(sceneSwitches).toHaveBeenCalledWith("scene-2");
  });

  it("is on by default once the compositor runs", async () => {
    render(StreamCrafter, { whipUrl: "https://ingest.example/whip" });

    expect(await screen.findByText("Scenes")).toBeTruthy();
    expect(screen.getByText("Default")).toBeTruthy();
  });

  it("can be turned off", async () => {
    const { container } = render(StreamCrafter, {
      whipUrl: "https://ingest.example/whip",
      showProducerPanel: false,
    });

    await waitFor(() => expect(controllers[0]?.enableCompositor).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve));

    expect(container.querySelector(".fw-sc-producer")).toBeNull();
  });

  it("stays away when the compositor is turned off", async () => {
    const { container } = render(StreamCrafter, {
      whipUrl: "https://ingest.example/whip",
      enableCompositor: false,
    });

    await waitFor(() => expect(controllers).toHaveLength(1));
    expect(container.querySelector(".fw-sc-producer")).toBeNull();
    expect(controllers[0].enableCompositor).not.toHaveBeenCalled();
  });

  it("gives composable children the compositor through context", async () => {
    render(ProducerPanelHost);

    expect(await screen.findByText("Default")).toBeTruthy();
  });

  it("moves the compositor onto the new controller when the endpoint changes", async () => {
    const { rerender } = render(StreamCrafter, {
      whipUrl: "https://ingest.example/first",
    });
    await waitFor(() => expect(controllers[0]?.enableCompositor).toHaveBeenCalled());

    await rerender({ whipUrl: "https://ingest.example/second" });

    await waitFor(() => expect(controllers).toHaveLength(2));
    await waitFor(() => expect(controllers[1].enableCompositor).toHaveBeenCalled());
  });
});
