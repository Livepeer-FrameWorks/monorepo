import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("WebCodecs worker path resolution", () => {
  it("tries source, published package, and host override worker paths", () => {
    const playerPath = path.resolve(__dirname, "../src/players/WebCodecsPlayer/index.ts");
    const source = fs.readFileSync(playerPath, "utf8");

    expect(source).toContain("./worker/decoder.worker.ts");
    expect(source).toContain("../../../workers/decoder.worker.js");
    expect(source).toContain("/workers/decoder.worker.js");
    expect(source).toContain("this.workerUrl");
    expect(source.indexOf("package dist worker")).toBeLessThan(source.indexOf("source worker"));
    expect(source).toContain('type: "debugging", value: false');
  });
});
