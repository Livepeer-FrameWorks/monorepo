import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/svelte";
import StreamSetupPanel from "$lib/components/stream-details/StreamSetupPanel.svelte";

afterEach(cleanup);

describe("stream source mode setup", () => {
  it("shows pull-source state without publisher URLs or keys", () => {
    render(StreamSetupPanel, {
      stream: {
        streamId: "stream-1",
        streamKey: null,
        ingestMode: "PULL",
        pullSource: { sourceUriRedacted: "rtsp://camera/***", enabled: true, class: "private" },
      },
    });

    expect(screen.getByText("Pull Source")).toBeTruthy();
    expect(screen.getByDisplayValue("rtsp://camera/***")).toBeTruthy();
    expect(screen.queryByText("Generic entry URLs")).toBeNull();
    expect(screen.queryByText("Stream Keys Management")).toBeNull();
  });

  it("shows only redacted managed-source metadata", () => {
    render(StreamSetupPanel, {
      stream: {
        streamId: "stream-2",
        streamKey: null,
        ingestMode: "MANAGED",
        managedSource: {
          sourceKind: "playlist",
          alwaysOn: true,
          placementCount: 1,
          allowedClusterIds: ["media-eu"],
        },
      },
    });

    expect(screen.getByText("Managed Source")).toBeTruthy();
    expect(screen.getByText(/playlist/i)).toBeTruthy();
    expect(screen.getByText("media-eu")).toBeTruthy();
    expect(
      screen.getByText(/Source paths and process commands are intentionally hidden/)
    ).toBeTruthy();
    expect(screen.queryByText("Generic entry URLs")).toBeNull();
    expect(screen.queryByText("Stream Keys Management")).toBeNull();
  });
});
