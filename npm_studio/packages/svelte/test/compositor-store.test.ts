import { describe, expect, it, vi } from "vitest";
import type { IngestControllerV2 } from "@livepeer-frameworks/streamcrafter-core";
import { createCompositorStore } from "../src/stores/compositor";

describe("createCompositorStore", () => {
  it("switches the renderer through the scene manager", () => {
    const setRenderer = vi.fn();
    const controller = {
      getSceneManager: () => ({ setRenderer }),
    } as unknown as IngestControllerV2;
    const store = createCompositorStore({ controller, autoEnable: false });

    store.setRenderer("webgl");

    expect(setRenderer).toHaveBeenCalledWith("webgl");
    store.destroy();
  });
});
