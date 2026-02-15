import { describe, it, expect, beforeEach } from "vitest";
import { FwSeekBar } from "../src/components/fw-seek-bar.js";

describe("FwSeekBar", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("maps pointer position to live DVR time range", async () => {
    const el = new FwSeekBar();
    el.isLive = true;
    el.seekableStart = 100;
    el.liveEdge = 200;
    el.duration = 200;

    document.body.appendChild(el);
    await el.updateComplete;

    const root = el.renderRoot.querySelector(".seek-root") as HTMLDivElement;
    root.getBoundingClientRect = () =>
      ({
        left: 0,
        width: 100,
        top: 0,
        bottom: 0,
        right: 100,
        height: 24,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      }) as DOMRect;

    const events: number[] = [];
    el.addEventListener("fw-seek", (event: Event) => {
      const typed = event as CustomEvent<{ time: number }>;
      events.push(typed.detail.time);
    });

    (el as unknown as { _onPointerDown: (event: PointerEvent) => void })._onPointerDown({
      preventDefault: () => {},
      clientX: 50,
      pointerId: 1,
    } as PointerEvent);

    expect(events).toHaveLength(1);
    expect(events[0]).toBeCloseTo(150, 4);
  });

  it("defers seek emission until release when commitOnRelease is true", async () => {
    const el = new FwSeekBar();
    el.isLive = true;
    el.seekableStart = 100;
    el.liveEdge = 200;
    el.duration = 200;
    el.commitOnRelease = true;

    document.body.appendChild(el);
    await el.updateComplete;

    const root = el.renderRoot.querySelector(".seek-root") as HTMLDivElement;
    root.getBoundingClientRect = () =>
      ({
        left: 0,
        width: 100,
        top: 0,
        bottom: 0,
        right: 100,
        height: 24,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      }) as DOMRect;

    const events: number[] = [];
    el.addEventListener("fw-seek", (event: Event) => {
      const typed = event as CustomEvent<{ time: number }>;
      events.push(typed.detail.time);
    });

    (el as unknown as { _onPointerDown: (event: PointerEvent) => void })._onPointerDown({
      preventDefault: () => {},
      clientX: 25,
      pointerId: 2,
    } as PointerEvent);

    (el as unknown as { _onGlobalPointerMove: (event: PointerEvent) => void })._onGlobalPointerMove(
      {
        clientX: 75,
        pointerId: 2,
      } as PointerEvent
    );

    expect(events).toHaveLength(0);

    (el as unknown as { _onGlobalPointerUp: (event: PointerEvent) => void })._onGlobalPointerUp({
      clientX: 75,
      pointerId: 2,
    } as PointerEvent);

    expect(events).toHaveLength(1);
    expect(events[0]).toBeCloseTo(175, 4);
  });
});
