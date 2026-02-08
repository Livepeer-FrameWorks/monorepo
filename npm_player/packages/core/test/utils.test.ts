import { describe, expect, it } from "vitest";

import { cn } from "../src/lib/utils";

describe("cn", () => {
  it("merges class names", () => {
    expect(cn("text-sm", "font-bold")).toBe("text-sm font-bold");
  });

  it("handles conditional classes", () => {
    expect(cn("text-sm", false && "hidden", true && "block")).toBe("text-sm block");
  });

  it("deduplicates tailwind classes", () => {
    expect(cn("p-2", "p-4", "p-2")).toBe("p-2");
  });
});
