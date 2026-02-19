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
