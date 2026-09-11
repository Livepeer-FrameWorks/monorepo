import { describe, it, expect, vi } from "vitest";
import { FwPlayer } from "../src/components/fw-player.js";

describe("FwPlayer", () => {
  it("restores document interaction handlers when the element reconnects", async () => {
    const add = vi.spyOn(document, "addEventListener");
    const remove = vi.spyOn(document, "removeEventListener");
    const player = new FwPlayer() as any;
    vi.spyOn(player.pc, "attach").mockResolvedValue(undefined);
    document.body.appendChild(player);
    await player.updateComplete;
    player.remove();
    document.body.appendChild(player);
    await player.updateComplete;
    for (const [event, handler] of [
      ["pointerdown", player._handleDocumentPointerDown],
      ["contextmenu", player._handleDocumentContextMenu],
      ["keydown", player._handleDocumentKeyDown],
    ]) {
      expect(
        add.mock.calls.filter((call) => call[0] === event && call[1] === handler)
      ).toHaveLength(2);
      expect(remove).toHaveBeenCalledWith(event, handler);
    }
    player.remove();
  });

  it("is a class that extends HTMLElement", () => {
    expect(FwPlayer).toBeDefined();
    expect(FwPlayer.prototype instanceof HTMLElement).toBe(true);
  });

  it("has the expected public API methods", () => {
    const proto = FwPlayer.prototype;
    expect(typeof proto.play).toBe("function");
    expect(typeof proto.pause).toBe("function");
    expect(typeof proto.togglePlay).toBe("function");
    expect(typeof proto.seek).toBe("function");
    expect(typeof proto.seekBy).toBe("function");
    expect(typeof proto.jumpToLive).toBe("function");
    expect(typeof proto.setVolume).toBe("function");
    expect(typeof proto.toggleMute).toBe("function");
    expect(typeof proto.toggleLoop).toBe("function");
    expect(typeof proto.toggleFullscreen).toBe("function");
    expect(typeof proto.togglePiP).toBe("function");
    expect(typeof proto.toggleSubtitles).toBe("function");
    expect(typeof proto.retry).toBe("function");
    expect(typeof proto.reload).toBe("function");
    expect(typeof proto.getQualities).toBe("function");
    expect(typeof proto.selectQuality).toBe("function");
    expect(typeof proto.destroy).toBe("function");
  });

  it("keeps custom controls when only legacy controls prop is set", () => {
    const player = new FwPlayer() as any;
    player.controls = true;
    player.stockControls = false;
    player.nativeControls = false;
    player.pc.s.currentPlayerInfo = null;

    expect(player._useStockControls).toBe(false);
  });

  it("uses stock controls when stock-controls is enabled", () => {
    const player = new FwPlayer() as any;
    player.controls = false;
    player.stockControls = true;
    player.nativeControls = false;
    player.pc.s.currentPlayerInfo = null;

    expect(player._useStockControls).toBe(true);
  });

  it("disables wrapper and stock controls when no-controls is enabled", () => {
    const player = new FwPlayer() as any;
    player.noControls = true;
    player.stockControls = true;
    player.nativeControls = true;
    player.pc.s.currentPlayerInfo = null;

    expect(player._controlsEnabled).toBe(false);
    expect(player._useStockControls).toBe(false);
  });
});
