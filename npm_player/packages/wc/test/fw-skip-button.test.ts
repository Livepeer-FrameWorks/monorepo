import { beforeEach, describe, expect, it, vi } from "vitest";
import { FwSkipButton } from "../src/components/controls/fw-skip-button.js";

describe("FwSkipButton", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("converts configured seconds to milliseconds before seekBy", () => {
    const seekBy = vi.fn();
    const el = new FwSkipButton() as any;
    el.direction = "forward";
    el.seconds = 10;
    el._player = { pc: { seekBy } };

    el.handleClick();

    expect(seekBy).toHaveBeenCalledWith(10000);
  });

  it("passes negative milliseconds for backward skip", () => {
    const seekBy = vi.fn();
    const el = new FwSkipButton() as any;
    el.direction = "back";
    el.seconds = 5;
    el._player = { pc: { seekBy } };

    el.handleClick();

    expect(seekBy).toHaveBeenCalledWith(-5000);
  });
});
