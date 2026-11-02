import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

const mocks = vi.hoisted(() => ({
  playerProps: [] as Array<Record<string, unknown>>,
  studioProps: [] as Array<Record<string, unknown>>,
  studioMounts: 0,
  studioUnmounts: 0,
}));

function fakeController() {
  const listeners = new Map<string, Set<(e: unknown) => void>>();
  return {
    listeners,
    on: vi.fn((event: string, listener: (e: unknown) => void) => {
      const set = listeners.get(event) ?? new Set();
      set.add(listener);
      listeners.set(event, set);
      return () => set.delete(listener);
    }),
    getVideoElement: () => null,
    getPlaybackQuality: () => ({
      bitrate: 2_500_000,
      bufferedAhead: 3200,
      latency: 1800,
      stallCount: 1,
      frameDropRate: 0.5,
    }),
    // The endpoint prefers WHEP while the player plays the HLS source.
    getEndpoints: () => ({ primary: { protocol: "WHEP", nodeId: "edge-1" } }),
    getState: () => "playing",
    getCurrentPlayerInfo: () => ({ name: "HLS.js Player", shortname: "hlsjs" }),
    getCurrentSourceInfo: () => ({ type: "html5/application/vnd.apple.mpegurl", url: "u" }),
    isEffectivelyLive: () => true,
    getTracks: () => [{ id: "video:0", kind: "video", label: "1080p", active: true }],
    getStreamState: () => null,
  };
}

vi.mock("@livepeer-frameworks/player-react", () => ({
  Player: React.forwardRef(function Player(props: Record<string, unknown>, ref) {
    mocks.playerProps.push(props);
    const controller = React.useMemo(fakeController, []);
    React.useImperativeHandle(ref, () => controller, [controller]);
    return <div data-testid="player" />;
  }),
}));

vi.mock("@livepeer-frameworks/streamcrafter-react", () => ({
  StreamCrafter: function StreamCrafter(props: Record<string, unknown>) {
    mocks.studioProps.push(props);
    React.useEffect(() => {
      mocks.studioMounts += 1;
      return () => {
        mocks.studioUnmounts += 1;
      };
    }, []);
    return <div data-testid="studio" />;
  },
}));

import { StreamTester } from "../src/StreamTester";

const setLocation = (search: string) =>
  window.history.replaceState(null, "", `/stream-test${search}`);

describe("StreamTester", () => {
  let writeText: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mocks.playerProps.length = 0;
    mocks.studioProps.length = 0;
    mocks.studioMounts = 0;
    mocks.studioUnmounts = 0;
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 404 })));
    writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  });

  afterEach(() => {
    cleanup();
    setLocation("");
    vi.unstubAllGlobals();
  });

  it("plays a shared ?url= link on arrival and offers a copy of it", async () => {
    setLocation("?url=https%3A%2F%2Fcdn.example.com%2Flive%2Findex.m3u8&protocol=HLS");
    render(<StreamTester />);

    await screen.findByTestId("player");
    const props = mocks.playerProps.at(-1)!;
    expect(props.contentId).toBe("pasted-url");
    expect(props.contentType).toBeUndefined();
    expect((props.options as Record<string, unknown>).devMode).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    await waitFor(() => expect(writeText).toHaveBeenCalled());
    const shared = new URL(writeText.mock.calls[0][0]);
    expect(shared.pathname).toBe("/stream-test");
    expect(shared.searchParams.get("url")).toBe("https://cdn.example.com/live/index.m3u8");
    expect(shared.searchParams.get("protocol")).toBe("HLS");
  });

  it("shows the player's diagnostics and event log beside the media", async () => {
    setLocation("?url=https%3A%2F%2Fcdn.example.com%2Flive%2Findex.m3u8");
    render(<StreamTester />);

    const panel = await screen.findByRole("complementary", { name: "Playback diagnostics" });
    await waitFor(() => expect(panel.textContent).toContain("HLS.js Player"));
    expect(panel.textContent).toContain("2.50 Mbps");
    expect(panel.textContent).toContain("3.2 s");
    expect(panel.textContent).toContain("1080p");
  });

  it("shows the protocol of the source that is playing, not the endpoint's first pick", async () => {
    setLocation("?url=https%3A%2F%2Fcdn.example.com%2Flive%2Findex.m3u8");
    render(<StreamTester />);

    const panel = await screen.findByRole("complementary", { name: "Playback diagnostics" });
    await waitFor(() => expect(panel.textContent).toContain("HLS.js Player"));
    const protocolRow = [...panel.querySelectorAll(".fwt-stat")].find(
      (row) => row.querySelector("dt")?.textContent === "Protocol"
    );
    expect(protocolRow?.querySelector("dd")?.textContent).toBe("HLS");
  });

  it("plays a sample from the picker", async () => {
    setLocation("");
    render(
      <StreamTester
        gatewayUrl="https://gw.example/graphql"
        samples={[{ id: "demo", label: "Demo", source: { mode: "playback", playbackId: "demo" } }]}
      />
    );

    fireEvent.change(screen.getByRole("combobox", { name: "Sample" }), {
      target: { value: "demo" },
    });
    await screen.findByTestId("player");
    const props = mocks.playerProps.at(-1)!;
    expect(props.contentId).toBe("demo");
    expect((props.options as Record<string, unknown>).gatewayUrl).toBe(
      "https://gw.example/graphql"
    );
  });

  it("publishes to a WHIP URL without putting it in the page link", async () => {
    setLocation("");
    render(<StreamTester />);

    fireEvent.click(screen.getByRole("tab", { name: "Publish" }));
    fireEvent.change(screen.getByLabelText("WHIP URL"), {
      target: { value: "https://edge.example.com/webrtc/secret-key" },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Open studio" }));
    });

    expect(screen.getByTestId("studio")).toBeTruthy();
    const props = mocks.studioProps.at(-1)!;
    expect(props.whipUrl).toBe("https://edge.example.com/webrtc/secret-key");
    expect(props.enableCompositor).toBe(true);
    expect(props).not.toHaveProperty("showProducerPanel");
    expect(window.location.search).toBe("");
  });

  it("keeps the studio broadcasting while the play tab is open", async () => {
    setLocation("");
    render(<StreamTester />);

    fireEvent.click(screen.getByRole("tab", { name: "Publish" }));
    fireEvent.change(screen.getByLabelText("WHIP URL"), {
      target: { value: "https://edge.example.com/webrtc/secret-key" },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Open studio" }));
    });
    expect(mocks.studioMounts).toBe(1);

    fireEvent.click(screen.getByRole("tab", { name: "Play" }));
    expect(mocks.studioUnmounts).toBe(0);
    expect(screen.getByTestId("studio")).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Publish" }));
    expect(mocks.studioMounts).toBe(1);
    expect(mocks.studioUnmounts).toBe(0);
  });
});
