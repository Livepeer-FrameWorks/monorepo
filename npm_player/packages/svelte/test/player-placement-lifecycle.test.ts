import { cleanup, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PlayerController } from "@livepeer-frameworks/player-core";
import Player from "../src/Player.svelte";
import { get } from "svelte/store";
import { createPlayerControllerStore } from "../src/stores/playerController";

const { instances } = vi.hoisted(() => ({ instances: [] as any[] }));
vi.mock("@livepeer-frameworks/player-core", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@livepeer-frameworks/player-core")>();
  return {
    ...actual,
    PlayerController: class extends actual.PlayerController {
      constructor(config: ConstructorParameters<typeof actual.PlayerController>[0]) {
        super(config);
        instances.push(this);
      }
    },
  };
});

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  );
});

afterEach(async () => {
  await cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  instances.length = 0;
});

describe("Svelte player placement lifecycle", () => {
  it("shows a terminal placement error instead of waiting for an endpoint", async () => {
    vi.spyOn(PlayerController.prototype, "attach").mockImplementation(async function () {
      (this as any).setState("error", { error: "Playback is unavailable" });
      (this as any).emit("error", { error: "Playback is unavailable" });
    });
    const view = render(Player, { contentId: "stopped-dvr", contentType: "dvr" });
    await waitFor(() =>
      expect(view.getByRole("alert").textContent).toContain("Playback is unavailable")
    );
    expect(view.queryByText("Waiting for stream...")).toBeNull();
  });

  it("retains store runtime options set before attachment and across reattachment", async () => {
    vi.spyOn(PlayerController.prototype, "attach").mockResolvedValue(undefined);
    const store = createPlayerControllerStore({ contentId: "playback", viewerProtocol: "HLS" });
    store.updateConfig({ muted: true, autoplay: false });
    expect(get(store).isMuted).toBe(true);
    await store.attach(document.createElement("div"));
    expect(instances[0].config).toMatchObject({ muted: true, autoplay: false });
    store.updateConfig({ muted: false });
    expect(get(store).isMuted).toBe(false);
    await store.attach(document.createElement("div"));
    expect(instances[1].config).toMatchObject({ muted: false, autoplay: false });
    store.destroy();
  });

  it("updates runtime options during pending placement without replacing admission", async () => {
    const attach = vi
      .spyOn(PlayerController.prototype, "attach")
      .mockImplementation(() => new Promise(() => {}));
    const update = vi.spyOn(PlayerController.prototype, "updateConfig");
    const view = render(Player, { contentId: "playback", options: { viewerProtocol: "HLS" } });
    await waitFor(() => expect(attach).toHaveBeenCalledTimes(1));
    await view.rerender({
      contentId: "playback",
      options: { viewerProtocol: "HLS", muted: true, autoplay: false },
    });
    await waitFor(() => expect(update).toHaveBeenLastCalledWith({ muted: true, autoplay: false }));
    expect(instances[0].isMuted()).toBe(true);
    await view.rerender({
      contentId: "playback",
      options: { viewerProtocol: "HLS", muted: true, autoplay: false, debug: true },
    });
    await waitFor(() => expect(update).toHaveBeenLastCalledWith({ debug: true }));
    await view.rerender({ contentId: "playback", options: { viewerProtocol: "HLS" } });
    await waitFor(() =>
      expect(update).toHaveBeenLastCalledWith({ muted: false, autoplay: true, debug: false })
    );
    expect(instances[0].isMuted()).toBe(false);
    expect(attach).toHaveBeenCalledTimes(1);
  });

  it("ignores a superseded attachment failure", async () => {
    let rejectOld!: (error: Error) => void;
    const old = new Promise<void>((_resolve, reject) => {
      rejectOld = reject;
    });
    const attach = vi
      .spyOn(PlayerController.prototype, "attach")
      .mockReturnValueOnce(old)
      .mockResolvedValue(undefined);
    const report = vi.spyOn(console, "error").mockImplementation(() => {});
    const view = render(Player, { contentId: "old" });
    await waitFor(() => expect(attach).toHaveBeenCalledTimes(1));
    await view.rerender({ contentId: "new" });
    await waitFor(() => expect(attach).toHaveBeenCalledTimes(2));
    rejectOld(new Error("obsolete resolution failed"));
    await old.catch(() => {});
    await Promise.resolve();
    expect(report).not.toHaveBeenCalled();
    expect(instances[1].config.contentId).toBe("new");
  });

  it("replaces a pending attach when credentials or required format change", async () => {
    const attach = vi
      .spyOn(PlayerController.prototype, "attach")
      .mockImplementation(() => new Promise(() => {}));
    const destroy = vi.spyOn(PlayerController.prototype, "destroy");
    const view = render(Player, {
      contentId: "playback",
      options: { viewerProtocol: "HLS", playbackAuth: { token: "old" } },
    });
    await waitFor(() => expect(attach).toHaveBeenCalledTimes(1));
    const previous = instances[0];
    await view.rerender({
      contentId: "playback",
      options: { viewerProtocol: "DASH", playbackAuth: { token: "new" } },
    });
    await waitFor(() => expect(attach).toHaveBeenCalledTimes(2));
    expect(destroy).toHaveBeenCalledTimes(1);
    expect(previous.isDestroyed).toBe(true);
    expect(instances[1].config).toMatchObject({
      viewerProtocol: "DASH",
      playbackAuth: { token: "new" },
    });
  });

  it("reports a current attachment failure", async () => {
    const error = new Error("current placement unavailable");
    vi.spyOn(PlayerController.prototype, "attach").mockRejectedValue(error);
    const report = vi.spyOn(console, "error").mockImplementation(() => {});
    render(Player, { contentId: "playback" });
    await waitFor(() =>
      expect(report).toHaveBeenCalledWith("[Player.svelte] attach failed:", error)
    );
  });

  it("does not recreate playback for equivalent requests or display-only changes", async () => {
    const attach = vi.spyOn(PlayerController.prototype, "attach").mockResolvedValue(undefined);
    const view = render(Player, {
      contentId: "playback",
      options: { viewerProtocol: "HLS", playbackAuth: { token: "same" } },
    });
    await waitFor(() => expect(attach).toHaveBeenCalledTimes(1));
    await view.rerender({
      contentId: "playback",
      options: {
        viewerProtocol: "HLS",
        playbackAuth: { token: "same" },
        locale: "en",
        debug: true,
      },
    });
    expect(attach).toHaveBeenCalledTimes(1);
  });

  it("destroys the component-owned controller on unmount", async () => {
    const attach = vi.spyOn(PlayerController.prototype, "attach").mockResolvedValue(undefined);
    const destroy = vi.spyOn(PlayerController.prototype, "destroy");
    const view = render(Player, { contentId: "playback" });
    await waitFor(() => expect(attach).toHaveBeenCalledTimes(1));
    await view.unmount();
    expect(destroy).toHaveBeenCalledOnce();
  });
});
