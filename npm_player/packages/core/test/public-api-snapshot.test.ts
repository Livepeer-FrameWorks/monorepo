import * as core from "../src/index";
import { describe, expect, it } from "vitest";

describe("public API snapshot", () => {
  it("keeps exported symbol surface stable", () => {
    const exportKeys = Object.keys(core).sort();

    expect(exportKeys).toMatchSnapshot();
  });
});
