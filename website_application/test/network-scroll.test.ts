// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import type { Map as MapLibreMap } from "maplibre-gl";
import { bindModifierScrollZoom } from "../../packages/map-core/index";

describe("shared map scroll policy", () => {
  it.each(["altKey", "ctrlKey", "metaKey"])(
    "requires %s for wheel zoom and releases normal page scrolling afterwards",
    (modifier) => {
      const element = document.createElement("div");
      const enable = vi.fn(),
        disable = vi.fn(),
        hint = vi.fn();
      const release = bindModifierScrollZoom(
        { getContainer: () => element, scrollZoom: { enable, disable } } as unknown as MapLibreMap,
        hint
      );
      const modified = new WheelEvent("wheel", { [modifier]: true, cancelable: true });
      element.dispatchEvent(modified);
      expect(modified.defaultPrevented).toBe(true);
      expect(enable).toHaveBeenCalledTimes(1);
      expect(hint).toHaveBeenCalledTimes(1);
      const ordinary = new WheelEvent("wheel", { cancelable: true });
      element.dispatchEvent(ordinary);
      expect(ordinary.defaultPrevented).toBe(false);
      expect(disable).toHaveBeenCalledTimes(1);
      release();
      element.dispatchEvent(new WheelEvent("wheel", { [modifier]: true, cancelable: true }));
      expect(enable).toHaveBeenCalledTimes(1);
    }
  );
});
