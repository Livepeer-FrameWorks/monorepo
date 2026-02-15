import { afterEach, describe, expect, it, vi } from "vitest";
import { FwScLayerList } from "../src/components/fw-sc-layer-list.js";

function createElement() {
  const el = new FwScLayerList();
  el.layers = [
    {
      id: "layer-1",
      sourceId: "source-1",
      zIndex: 1,
      visible: true,
      transform: { opacity: 1 },
    },
    {
      id: "layer-2",
      sourceId: "source-2",
      zIndex: 2,
      visible: true,
      transform: { opacity: 0.8 },
    },
  ] as any;
  el.sources = [
    { id: "source-1", label: "Camera", type: "camera" },
    { id: "source-2", label: "Screen", type: "screen" },
  ] as any;
  document.body.appendChild(el);
  return el;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("FwScLayerList parity", () => {
  it("hides optional transform/remove controls when handlers are not provided", async () => {
    const onVisibilityToggle = vi.fn();
    const onReorder = vi.fn();
    const el = createElement();
    el.onVisibilityToggle = onVisibilityToggle;
    el.onReorder = onReorder;
    await el.updateComplete;

    expect(el.shadowRoot?.querySelector('button[title="Edit opacity"]')).toBeNull();
    expect(el.shadowRoot?.querySelector('button[title="Remove layer"]')).toBeNull();
  });

  it("shows and wires optional transform/remove controls when handlers are provided", async () => {
    const onVisibilityToggle = vi.fn();
    const onReorder = vi.fn();
    const onTransformEdit = vi.fn();
    const onRemove = vi.fn();

    const el = createElement();
    el.onVisibilityToggle = onVisibilityToggle;
    el.onReorder = onReorder;
    el.onTransformEdit = onTransformEdit;
    el.onRemove = onRemove;
    await el.updateComplete;

    const editButton = el.shadowRoot?.querySelector(
      'button[title="Edit opacity"]'
    ) as HTMLButtonElement | null;
    expect(editButton).not.toBeNull();
    editButton?.click();
    await el.updateComplete;

    const slider = el.shadowRoot?.querySelector(
      ".fw-sc-layer-opacity input[type='range']"
    ) as HTMLInputElement | null;
    expect(slider).not.toBeNull();
    if (slider) {
      slider.value = "0.5";
      slider.dispatchEvent(new Event("input", { bubbles: true }));
    }
    expect(onTransformEdit).toHaveBeenCalledWith("layer-2", { opacity: 0.5 });

    const removeButton = el.shadowRoot?.querySelector(
      'button[title="Remove layer"]'
    ) as HTMLButtonElement | null;
    expect(removeButton).not.toBeNull();
    removeButton?.click();
    expect(onRemove).toHaveBeenCalledWith("layer-2");
  });

  it("toggles onSelect payload based on currently selected layer", async () => {
    const onVisibilityToggle = vi.fn();
    const onReorder = vi.fn();
    const onSelect = vi.fn();

    const el = createElement();
    el.onVisibilityToggle = onVisibilityToggle;
    el.onReorder = onReorder;
    el.onSelect = onSelect;
    await el.updateComplete;

    const rows = el.shadowRoot?.querySelectorAll(".fw-sc-layer-item");
    const firstRow = rows?.[0] as HTMLElement | undefined;
    firstRow?.click();
    expect(onSelect).toHaveBeenLastCalledWith("layer-2");

    el.selectedLayerId = "layer-2";
    await el.updateComplete;
    firstRow?.click();
    expect(onSelect).toHaveBeenLastCalledWith(null);
  });

  it("calls onReorder when move controls are used", async () => {
    const onVisibilityToggle = vi.fn();
    const onReorder = vi.fn();

    const el = createElement();
    el.onVisibilityToggle = onVisibilityToggle;
    el.onReorder = onReorder;
    await el.updateComplete;

    const moveDownButtons = el.shadowRoot?.querySelectorAll('button[title="Move down"]');
    const firstMoveDown = moveDownButtons?.[0] as HTMLButtonElement | undefined;
    firstMoveDown?.click();

    expect(onReorder).toHaveBeenCalledWith(["layer-1", "layer-2"]);
  });
});
