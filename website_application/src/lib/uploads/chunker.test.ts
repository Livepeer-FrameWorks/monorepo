import { describe, expect, it } from "vitest";
import { partByteRange, sliceFilePart } from "./chunker";

describe("partByteRange", () => {
  it("handles even part boundaries", () => {
    expect(partByteRange(1, 100, 300)).toEqual({ start: 0, end: 100, size: 100 });
    expect(partByteRange(2, 100, 300)).toEqual({ start: 100, end: 200, size: 100 });
    expect(partByteRange(3, 100, 300)).toEqual({ start: 200, end: 300, size: 100 });
  });

  it("clamps the final part to total size", () => {
    expect(partByteRange(3, 100, 250)).toEqual({ start: 200, end: 250, size: 50 });
  });

  it("returns size 0 past EOF", () => {
    expect(partByteRange(5, 100, 250)).toEqual({ start: 400, end: 250, size: -150 });
  });
});

describe("sliceFilePart", () => {
  it("returns the right slice of a Blob", async () => {
    const blob = new Blob(["abcdefghij"]);
    const part1 = sliceFilePart(blob, 1, 4);
    const part2 = sliceFilePart(blob, 2, 4);
    const part3 = sliceFilePart(blob, 3, 4);
    expect(await part1.text()).toBe("abcd");
    expect(await part2.text()).toBe("efgh");
    expect(await part3.text()).toBe("ij");
  });
});
