import { beforeEach, describe, expect, it, vi } from "vitest";
import { createTranslator } from "@livepeer-frameworks/player-core";
import { FwVolumeControl } from "../src/components/fw-volume-control.js";

const defaultT = createTranslator({ locale: "en" });

function createMockPc() {
  return {
    s: {
      isMuted: false,
      volume: 0.5,
      videoElement: null,
    },
    t: defaultT,
    setVolume: vi.fn(),
    toggleMute: vi.fn(),
  };
}

describe("FwVolumeControl", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("keeps slider expanded while dragging and updates volume on pointer move", async () => {
    const pc = createMockPc();
    const el = new FwVolumeControl() as any;
    el.pc = pc;
    document.body.appendChild(el);
    await el.updateComplete;

    const slider = document.createElement("div") as HTMLElement & {
      _capturedPointerId?: number | null;
      hasPointerCapture: (pointerId: number) => boolean;
      setPointerCapture: (pointerId: number) => void;
      releasePointerCapture: (pointerId: number) => void;
    };
    slider.getBoundingClientRect = () =>
      ({ left: 10, width: 100, top: 0, right: 110, bottom: 10, height: 10 }) as DOMRect;
    slider.setPointerCapture = (pointerId: number) => {
      slider._capturedPointerId = pointerId;
    };
    slider.hasPointerCapture = (pointerId: number) => slider._capturedPointerId === pointerId;
    slider.releasePointerCapture = (pointerId: number) => {
      if (slider._capturedPointerId === pointerId) {
        slider._capturedPointerId = null;
      }
    };

    el._onSliderPointerDown({
      pointerId: 1,
      clientX: 60,
      currentTarget: slider,
      preventDefault: vi.fn(),
    } as unknown as PointerEvent);

    expect(el._dragging).toBe(true);
    expect(el._expanded).toBe(true);
    expect(pc.setVolume).toHaveBeenLastCalledWith(0.5);

    el._handleMouseLeave();
    expect(el._expanded).toBe(true);

    el._onGlobalPointerMove({
      pointerId: 1,
      clientX: 110,
    } as unknown as PointerEvent);

    expect(pc.setVolume).toHaveBeenLastCalledWith(1);

    el._onGlobalPointerUp({
      pointerId: 1,
    } as unknown as PointerEvent);

    expect(el._dragging).toBe(false);
    expect(el._activePointerId).toBeNull();
    expect(el._activeSliderTarget).toBeNull();
  });
});
