import { describe, it, expect, vi } from "vitest";
import { FwPlayer } from "../src/components/fw-player.js";

describe("FwPlayer context menu", () => {
  it("keeps the context menu open on pointerdown inside menu composed path", () => {
    const player = new FwPlayer() as any;
    const menu = document.createElement("div");

    player._contextMenuOpen = true;
    player._getContextMenuElement = () => menu;

    const event = {
      composedPath: () => [menu, player, document.body, document, window],
    } as unknown as PointerEvent;

    player._handleDocumentPointerDown(event);

    expect(player._contextMenuOpen).toBe(true);
  });

  it("closes the context menu on outside pointerdown", () => {
    const player = new FwPlayer() as any;
    const menu = document.createElement("div");

    player._contextMenuOpen = true;
    player._getContextMenuElement = () => menu;

    const event = {
      composedPath: () => [document.body, document, window],
    } as unknown as PointerEvent;

    player._handleDocumentPointerDown(event);

    expect(player._contextMenuOpen).toBe(false);
  });

  it("clamps context menu coordinates to player bounds", () => {
    const player = new FwPlayer() as any;
    const preventDefault = vi.fn();
    const playerRect = {
      left: 120,
      top: 80,
      width: 320,
      height: 200,
      right: 440,
      bottom: 280,
    };
    const largeX = playerRect.right + 400;
    const largeY = playerRect.bottom + 400;

    player.getBoundingClientRect = () => playerRect as DOMRect;

    player._handleContextMenu({
      preventDefault,
      clientX: largeX,
      clientY: largeY,
      target: document.createElement("div"),
    } as unknown as MouseEvent);

    expect(preventDefault).toHaveBeenCalledTimes(1);
    expect(player._contextMenuOpen).toBe(true);
    expect(player._contextMenuX).toBeLessThanOrEqual(playerRect.width - 8);
    expect(player._contextMenuY).toBeLessThanOrEqual(playerRect.height - 8);
    expect(player._contextMenuX).toBeGreaterThanOrEqual(8);
    expect(player._contextMenuY).toBeGreaterThanOrEqual(8);
  });

  it("closes the context menu on Escape", () => {
    const player = new FwPlayer() as any;
    player._contextMenuOpen = true;

    const event = new KeyboardEvent("keydown", { key: "Escape" });
    player._handleDocumentKeyDown(event);

    expect(player._contextMenuOpen).toBe(false);
  });

  it("opens the context menu with Shift+F10", () => {
    const player = new FwPlayer() as any;
    const preventDefault = vi.fn();

    player._handleContextMenuShortcut({
      key: "F10",
      shiftKey: true,
      preventDefault,
    } as unknown as KeyboardEvent);

    expect(preventDefault).toHaveBeenCalledTimes(1);
    expect(player._contextMenuOpen).toBe(true);
  });

  it("transitions through closed state before unmounting menu", () => {
    vi.useFakeTimers();
    const player = new FwPlayer() as any;

    player._openContextMenu(32, 24);
    expect(player._contextMenuMounted).toBe(true);
    expect(player._contextMenuState).toBe("open");

    player._closeContextMenu();
    expect(player._contextMenuOpen).toBe(false);
    expect(player._contextMenuMounted).toBe(true);
    expect(player._contextMenuState).toBe("closed");

    vi.advanceTimersByTime(200);
    expect(player._contextMenuMounted).toBe(false);
    vi.useRealTimers();
  });

  it("sets collision side metadata when opening near viewport edge", () => {
    const player = new FwPlayer() as any;

    player._openContextMenu(window.innerWidth + 400, window.innerHeight + 400);

    expect(["top", "left", "bottom", "right"]).toContain(player._contextMenuSide);
    expect(player._contextMenuSide).not.toBe("bottom");
  });

  it("supports first-character typeahead navigation", () => {
    const player = new FwPlayer() as any;
    const stats = document.createElement("button");
    stats.textContent = "Stats";
    stats.focus = vi.fn();
    const pip = document.createElement("button");
    pip.textContent = "Picture-in-Picture";
    pip.focus = vi.fn();
    const loop = document.createElement("button");
    loop.textContent = "Enable Loop";
    loop.focus = vi.fn();

    player._contextMenuOpen = true;
    player._getContextMenuItems = () => [stats, pip, loop];

    player._handleContextMenuKeyDown({
      key: "p",
      preventDefault: vi.fn(),
      metaKey: false,
      ctrlKey: false,
      altKey: false,
    } as unknown as KeyboardEvent);

    expect(pip.focus as unknown as ReturnType<typeof vi.fn>).toHaveBeenCalledTimes(1);
  });
});
