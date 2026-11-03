import { cleanup, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PlayerController } from "@livepeer-frameworks/player-core";
import Player from "../src/Player.svelte";
import ControllerHost from "./fixtures/ControllerHost.svelte";

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  );
  vi.spyOn(PlayerController.prototype, "attach").mockResolvedValue(undefined);
});

afterEach(async () => {
  await cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("Svelte Player controller", () => {
  it("is handed to bind:controller, as the React ref is, and replaced with the content", async () => {
    const seen: (PlayerController | null)[] = [];
    const view = render(ControllerHost, {
      contentId: "first",
      onController: (controller) => seen.push(controller),
    });

    await waitFor(() => expect(seen.at(-1)).toBeInstanceOf(PlayerController));
    const first = seen.at(-1);

    await view.rerender({ contentId: "second", onController: (c) => seen.push(c) });
    await waitFor(() => expect(seen.at(-1)).not.toBe(first));
    expect(seen.at(-1)).toBeInstanceOf(PlayerController);
  });

  it("is returned by getController on the component", async () => {
    const view = render(Player, {
      contentId: "playback",
      contentType: "live",
      options: { mistUrl: "https://edge.example" },
    });

    await waitFor(() =>
      expect(
        (view.component as unknown as { getController(): unknown }).getController()
      ).toBeInstanceOf(PlayerController)
    );
  });
});
