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

  it("offers the producer panel behind an attribute, off by default", () => {
    expect(new FwStreamCrafter().showProducerPanel).toBe(false);
    expect(source).toContain('attribute: "show-producer-panel"');
    expect(source).toContain("<fw-sc-scene-switcher");
    expect(source).toContain("<fw-sc-layer-list");
  });

  it("keeps Add Camera available with a camera live and offers the other cameras", async () => {
    expect(source).not.toContain("!canAddSource || hasCamera");

    const el = new FwStreamCrafter();
    const startCamera = vi.fn(async () => ({}));
    Object.assign(el.pc, {
      startCamera,
      getCameraChoices: async () => [
        { deviceId: "front", label: "Front", inUse: true },
        { deviceId: "rear", label: "Rear", inUse: false },
      ],
    });
    el.pc.s.sources = [{ type: "camera" }] as never;
    const picker = el as unknown as {
      _handleAddCamera(): Promise<void>;
      _startCamera(deviceId?: string): void;
      _cameraChoices: unknown[] | null;
    };

    await picker._handleAddCamera();
    expect(picker._cameraChoices).toHaveLength(2);
    expect(startCamera).not.toHaveBeenCalled();

    picker._startCamera("rear");
    expect(startCamera).toHaveBeenCalledWith({ videoDeviceId: "rear" });
    expect(picker._cameraChoices).toBeNull();
  });

  it("starts the only camera straight away", async () => {
    const el = new FwStreamCrafter();
    const startCamera = vi.fn(async () => ({}));
    Object.assign(el.pc, {
      startCamera,
      getCameraChoices: async () => [{ deviceId: "front", label: "Front", inUse: false }],
    });

    await (el as unknown as { _handleAddCamera(): Promise<void> })._handleAddCamera();

    expect(startCamera).toHaveBeenCalledWith({ videoDeviceId: "front" });
  });

  it("reports an image or media file it cannot use", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const el = new FwStreamCrafter();
    Object.assign(el.pc, {
      addMediaFile: async () => {
        throw new Error("unsupported");
      },
      getController: () => ({
        getSceneManager: () => ({
          addImageOverlay: async () => {
            throw new Error("bad image");
          },
        }),
      }),
    });
    const internals = el as unknown as {
      _handleOverlayImage(event: Event, scene: unknown): Promise<void>;
      _handleMediaFile(event: Event): Promise<void>;
      _overlayImageFailed: boolean;
      _mediaFileFailed: boolean;
    };
    const pick = () =>
      ({ target: { files: [new File(["x"], "file")], value: "file" } }) as unknown as Event;

    await internals._handleOverlayImage(pick(), { id: "scene" });
    await internals._handleMediaFile(pick());

    expect(internals._overlayImageFailed).toBe(true);
    expect(internals._mediaFileFailed).toBe(true);
  });

  it("offers overlays and media files in the producer panel", () => {
    expect(source).toContain('accept="image/*"');
    expect(source).toContain('accept="video/*,audio/*"');
    expect(source).toContain('this._t("mediaTabNotice")');
    expect(source).toContain("setOverlayPlacement");
    expect(source).toContain("updateTextOverlay");
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
