import fs from "node:fs";
import path from "node:path";
import { describe, it, expect, vi } from "vitest";
import { FwStreamCrafter } from "../src/components/fw-streamcrafter.js";
import {
  STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS,
  STREAMCRAFTER_COMPONENT_PARITY_CONTEXT_MENU_I18N_KEYS,
  STREAMCRAFTER_WRAPPER_PARITY_ACTION_METHODS,
} from "../../test-contract/streamcrafter-wrapper-contract";

describe("FwStreamCrafter", () => {
  const streamCrafterPath = path.resolve(__dirname, "../src/components/fw-streamcrafter.ts");
  const source = fs.readFileSync(streamCrafterPath, "utf8");

  it("is a class that extends HTMLElement", () => {
    expect(FwStreamCrafter).toBeDefined();
    expect(FwStreamCrafter.prototype instanceof HTMLElement).toBe(true);
  });

  it("has the expected public API methods", () => {
    const proto = FwStreamCrafter.prototype;

    for (const actionName of STREAMCRAFTER_WRAPPER_PARITY_ACTION_METHODS) {
      expect(typeof proto[actionName]).toBe("function");
    }

    expect(typeof proto.destroy).toBe("function");
  });

  it("shows the producer panel and runs the compositor by default", () => {
    const el = new FwStreamCrafter();
    expect(el.showProducerPanel).toBe(true);
    expect(el.enableCompositor).toBe(true);
  });

  it("turns the producer panel and the compositor off with a false attribute", async () => {
    const el = document.createElement("fw-streamcrafter") as FwStreamCrafter;
    el.setAttribute("show-producer-panel", "false");
    el.setAttribute("enable-compositor", "false");
    document.body.appendChild(el);
    await el.updateComplete;

    expect(el.showProducerPanel).toBe(false);
    expect(el.enableCompositor).toBe(false);

    el.setAttribute("show-producer-panel", "");
    await el.updateComplete;
    expect(el.showProducerPanel).toBe(true);
    el.remove();
  });

  it("renders again when the scene manager changes the scenes", () => {
    const el = new FwStreamCrafter();
    const handlers = new Map<string, () => void>();
    const sceneManager = {
      on: (event: string, handler: () => void) => {
        handlers.set(event, handler);
        return () => handlers.delete(event);
      },
    };
    const requestUpdate = vi.spyOn(el, "requestUpdate");
    const internals = el as unknown as { _bindSceneManager(sm: unknown): void };

    internals._bindSceneManager(sceneManager);
    handlers.get("layerAdded")?.();
    handlers.get("sceneActivated")?.();
    expect(requestUpdate).toHaveBeenCalledTimes(2);

    internals._bindSceneManager(null);
    expect(handlers.size).toBe(0);
  });

  it("masks the stream key in the copied stream info", async () => {
    const writeText = vi.fn(async (_text: string) => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const el = document.createElement("fw-streamcrafter") as FwStreamCrafter;
    el.setAttribute("whip-url", "https://edge.example/webrtc/secret-key");
    el.setAttribute("enable-compositor", "false");
    document.body.appendChild(el);
    await el.updateComplete;

    el.shadowRoot!.querySelector(".fw-sc-root")!.dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, composed: true })
    );
    await el.updateComplete;
    const copy = [
      ...el.shadowRoot!.querySelectorAll<HTMLButtonElement>(".fw-sc-context-menu-item"),
    ].find((button) => button.textContent?.trim() === "Copy Stream Info");
    copy!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));

    expect(writeText).toHaveBeenCalledTimes(1);
    const copied = writeText.mock.calls[0][0];
    expect(copied).toContain("WHIP: https://edge.example/webrtc/•••");
    expect(copied).not.toContain("secret-key");
    el.remove();
  });

  it("keeps theme colours in tokens", () => {
    expect(source).not.toMatch(/#(1a1b26|565f89|7aa2f7|a9b1d6|c0caf5|9ece6a|f7768e)/i);
  });

  it("references shared context-menu i18n keys", () => {
    for (const key of STREAMCRAFTER_COMPONENT_PARITY_CONTEXT_MENU_I18N_KEYS) {
      expect(source).toContain(`"${key}"`);
    }
  });

  it("invokes shared callback markers from event bridge", () => {
    expect(source).toContain(`${STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS.onStateChange}?.(`);
    expect(source).toContain(`${STREAMCRAFTER_COMPONENT_PARITY_CALLBACK_MARKERS.onError}?.(`);
  });

  it("forwards bridge events to callback props", () => {
    const el = new FwStreamCrafter();
    const onStateChange = vi.fn();
    const onError = vi.fn();

    el.onStateChange = onStateChange;
    el.onError = onError;
    el.connectedCallback();

    el.dispatchEvent(
      new CustomEvent("fw-sc-state-change", {
        detail: { state: "streaming", context: { source: "test" } },
      })
    );
    el.dispatchEvent(new CustomEvent("fw-sc-error", { detail: { error: "boom" } }));

    expect(onStateChange).toHaveBeenCalledWith("streaming", { source: "test" });
    expect(onError).toHaveBeenCalledWith("boom");

    el.disconnectedCallback();
  });

  it("keeps context menu open for inside clicks via composedPath", () => {
    const el = new FwStreamCrafter() as any;
    const menu = document.createElement("div");
    el._contextMenu = { x: 10, y: 12 };
    Object.defineProperty(el, "_contextMenuEl", { value: menu, configurable: true });

    el._handleDocumentMouseDown({
      composedPath: () => [menu],
      target: null,
    } as unknown as MouseEvent);

    expect(el._contextMenu).toEqual({ x: 10, y: 12 });
  });

  it("closes context menu for outside clicks", () => {
    const el = new FwStreamCrafter() as any;
    const menu = document.createElement("div");
    el._contextMenu = { x: 10, y: 12 };
    Object.defineProperty(el, "_contextMenuEl", { value: menu, configurable: true });

    el._handleDocumentMouseDown({
      composedPath: () => [],
      target: document.createElement("button"),
    } as unknown as MouseEvent);

    expect(el._contextMenu).toBeNull();
  });
});
